// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test, Vm} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

/// Admin hardening — what a compromised or lost admin key can and cannot do.
///
/// Before this, setCaps applied instantly with no upper bound and admin could never move, so
/// a leaked admin key could raise the lifetime loss ceiling and lose the pool in one block,
/// and there was no way to take the role back. Two controls close that:
///
///   1. raising a cap is queued for CAP_RAISE_DELAY; lowering is immediate and cancels a
///      queued raise, so the real key holder and the LPs (withdraw is never paused) get a
///      window before a larger loss budget exists;
///   2. admin moves only by transferAdmin + acceptAdmin after ADMIN_TRANSFER_DELAY, on both
///      contracts, so a leaked key cannot rotate the real holder out before they can cancel.
contract AdminHardeningTest is Test {
    JobVault internal vault;
    FloatPool internal pool;
    MockUSDC internal usdc;

    address internal constant ADMIN    = address(0xA0);
    address internal constant NEXT     = address(0xA1);
    address internal constant STRANGER = address(0xBAD);
    address internal constant LP       = address(0x1D);
    address internal constant ATTACKER = address(0xE71);

    uint256 internal constant EXPOSURE = 50_000_000;
    uint256 internal constant ADVANCE  = 10_000_000;

    event CapsSet(uint256 maxTotalExposure, uint256 maxAdvance);
    event CapsRaiseQueued(uint256 maxTotalExposure, uint256 maxAdvance, uint64 executableAt);
    event CapsRaiseCancelled();
    event AdminTransferStarted(address indexed currentAdmin, address indexed pendingAdmin, uint64 acceptableAt);
    event AdminTransferCancelled();
    event AdminTransferred(address indexed previousAdmin, address indexed newAdmin);

    function setUp() public {
        usdc = new MockUSDC();
        vm.startPrank(ADMIN);
        vault = new JobVault(IERC20(address(usdc)));
        pool = new FloatPool(IERC20(address(usdc)));
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));
        pool.setCaps(EXPOSURE, ADVANCE); // first configuration: immediate
        vm.stopPrank();
    }

    // ── 1. Cap raises are timelocked; lowering is not ───────────────────

    function test_firstConfigurationIsImmediate() public view {
        assertEq(pool.maxTotalExposure(), EXPOSURE);
        assertEq(pool.maxAdvance(), ADVANCE);
        assertEq(pool.capRaiseExecutableAt(), 0);
    }

    function test_raiseIsQueuedNotApplied() public {
        uint64 at = uint64(block.timestamp + pool.CAP_RAISE_DELAY());
        vm.expectEmit(address(pool));
        emit CapsRaiseQueued(EXPOSURE * 10, ADVANCE, at);
        vm.prank(ADMIN);
        pool.setCaps(EXPOSURE * 10, ADVANCE);

        assertEq(pool.maxTotalExposure(), EXPOSURE, "a raise must not take effect immediately");
        assertEq(pool.pendingMaxTotalExposure(), EXPOSURE * 10);
        assertEq(pool.capRaiseExecutableAt(), at);
    }

    function test_raisingOnlyMaxAdvanceIsAlsoQueued() public {
        vm.prank(ADMIN);
        pool.setCaps(EXPOSURE, ADVANCE * 2);
        assertEq(pool.maxAdvance(), ADVANCE);
        assertGt(pool.capRaiseExecutableAt(), 0);
    }

    function test_raiseCannotExecuteBeforeTheDelay() public {
        vm.startPrank(ADMIN);
        pool.setCaps(EXPOSURE * 10, ADVANCE);
        vm.warp(block.timestamp + pool.CAP_RAISE_DELAY() - 1);
        vm.expectRevert(FloatPool.CapRaiseTimelocked.selector);
        pool.executeCapsRaise();
        vm.stopPrank();
        assertEq(pool.maxTotalExposure(), EXPOSURE);
    }

    function test_raiseExecutesAfterTheDelay() public {
        vm.startPrank(ADMIN);
        pool.setCaps(EXPOSURE * 10, ADVANCE * 2);
        vm.warp(block.timestamp + pool.CAP_RAISE_DELAY());
        vm.recordLogs();
        pool.executeCapsRaise();
        vm.stopPrank();

        // A raise taking effect must read as a raise taking effect. A monitor watching for
        // CapsRaiseCancelled as "countered" must never see it on the execute path.
        Vm.Log[] memory logs = vm.getRecordedLogs();
        assertEq(logs.length, 1, "execute emits exactly one event");
        assertEq(logs[0].topics[0], keccak256("CapsSet(uint256,uint256)"));

        assertEq(pool.maxTotalExposure(), EXPOSURE * 10);
        assertEq(pool.maxAdvance(), ADVANCE * 2);
        assertEq(pool.capRaiseExecutableAt(), 0, "the queue is consumed");
    }

    function test_executeWithNothingQueuedReverts() public {
        vm.prank(ADMIN);
        vm.expectRevert(FloatPool.NoCapRaiseQueued.selector);
        pool.executeCapsRaise();
    }

    function test_loweringIsImmediateAndCancelsAQueuedRaise() public {
        vm.startPrank(ADMIN);
        pool.setCaps(EXPOSURE * 10, ADVANCE); // the attacker's raise
        vm.expectEmit(address(pool));
        emit CapsRaiseCancelled();
        pool.setCaps(EXPOSURE / 2, ADVANCE / 2); // the defender lowers
        vm.stopPrank();

        assertEq(pool.maxTotalExposure(), EXPOSURE / 2);
        assertEq(pool.maxAdvance(), ADVANCE / 2);
        assertEq(pool.capRaiseExecutableAt(), 0);

        vm.warp(block.timestamp + pool.CAP_RAISE_DELAY());
        vm.prank(ADMIN);
        vm.expectRevert(FloatPool.NoCapRaiseQueued.selector);
        pool.executeCapsRaise();
    }

    function test_cancelDropsTheRaiseAndKeepsCurrentCaps() public {
        vm.startPrank(ADMIN);
        pool.setCaps(EXPOSURE * 10, ADVANCE);
        vm.expectEmit(address(pool));
        emit CapsRaiseCancelled();
        pool.cancelCapsRaise();
        vm.expectRevert(FloatPool.NoCapRaiseQueued.selector);
        pool.cancelCapsRaise();
        vm.stopPrank();
        assertEq(pool.maxTotalExposure(), EXPOSURE);
    }

    function test_capQueueControlsAreAdminOnly() public {
        vm.prank(ADMIN);
        pool.setCaps(EXPOSURE * 10, ADVANCE);
        vm.warp(block.timestamp + pool.CAP_RAISE_DELAY());

        vm.startPrank(STRANGER);
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.executeCapsRaise();
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.cancelCapsRaise();
        vm.stopPrank();
    }

    /// The point of the delay: during it the LP can take idle capital out, so a compromised
    /// admin's raise lands on a pool that has already been emptied of what it could lose.
    function test_lpCanWithdrawWhileARaiseIsPending() public {
        vm.prank(ADMIN);
        pool.setDepositorAllowed(LP, true);
        usdc.mint(LP, 100_000_000);
        vm.startPrank(LP);
        usdc.approve(address(pool), type(uint256).max);
        pool.deposit(100_000_000, LP);
        vm.stopPrank();

        vm.startPrank(ADMIN);
        pool.setCaps(EXPOSURE * 10, ADVANCE * 10);
        pool.setPaused(true); // even paused, the exit stays open
        vm.stopPrank();

        vm.prank(LP);
        pool.withdraw(100_000_000, LP, LP);
        assertEq(usdc.balanceOf(LP), 100_000_000, "the LP is out before the raise can apply");
    }

    // ── 2. Two-step admin transfer, on both contracts ───────────────────

    function test_poolAdminTransferIsTwoStep() public {
        uint64 at = uint64(block.timestamp + pool.ADMIN_TRANSFER_DELAY());
        vm.expectEmit(address(pool));
        emit AdminTransferStarted(ADMIN, NEXT, at);
        vm.prank(ADMIN);
        pool.transferAdmin(NEXT);
        assertEq(pool.admin(), ADMIN, "nothing moves until the new admin accepts");

        vm.warp(at);
        vm.expectEmit(address(pool));
        emit AdminTransferred(ADMIN, NEXT);
        vm.prank(NEXT);
        pool.acceptAdmin();
        assertEq(pool.admin(), NEXT);
        assertEq(pool.pendingAdmin(), address(0));

        vm.prank(ADMIN);
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.setPaused(true);
        vm.prank(NEXT);
        pool.setPaused(true);
    }

    function test_vaultAdminTransferIsTwoStep() public {
        vm.prank(ADMIN);
        vault.transferAdmin(NEXT);
        assertEq(vault.admin(), ADMIN);
        vm.warp(block.timestamp + vault.ADMIN_TRANSFER_DELAY());
        vm.prank(NEXT);
        vault.acceptAdmin();
        assertEq(vault.admin(), NEXT);

        vm.prank(ADMIN);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.setPaused(true);
        vm.prank(NEXT);
        vault.setPaused(true);
    }

    function test_onlyThePendingAdminCanAccept() public {
        vm.prank(ADMIN);
        pool.transferAdmin(NEXT);
        vm.prank(STRANGER);
        vm.expectRevert(FloatPool.NotPendingAdmin.selector);
        pool.acceptAdmin();

        vm.prank(STRANGER);
        vm.expectRevert(JobVault.NotPendingAdmin.selector);
        vault.acceptAdmin();
    }

    function test_acceptWithNothingPendingReverts() public {
        vm.prank(address(0));
        vm.expectRevert(FloatPool.NotPendingAdmin.selector);
        pool.acceptAdmin();
        vm.prank(address(0));
        vm.expectRevert(JobVault.NotPendingAdmin.selector);
        vault.acceptAdmin();
    }

    function test_transferAdminIsAdminOnlyAndRejectsZero() public {
        vm.prank(STRANGER);
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.transferAdmin(STRANGER);
        vm.prank(STRANGER);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.transferAdmin(STRANGER);

        vm.startPrank(ADMIN);
        vm.expectRevert(FloatPool.ZeroAddress.selector);
        pool.transferAdmin(address(0));
        vm.expectRevert(JobVault.ZeroAddress.selector);
        vault.transferAdmin(address(0));
        vm.stopPrank();
    }

    function test_transferToSelfCancelsAPendingTransfer() public {
        vm.startPrank(ADMIN);
        pool.transferAdmin(NEXT);
        vm.expectEmit(address(pool));
        emit AdminTransferCancelled();
        pool.transferAdmin(ADMIN);
        vault.transferAdmin(NEXT);
        vault.transferAdmin(ADMIN);
        vm.stopPrank();
        assertEq(pool.pendingAdmin(), address(0));
        assertEq(vault.pendingAdmin(), address(0));

        vm.warp(block.timestamp + pool.ADMIN_TRANSFER_DELAY());
        vm.startPrank(NEXT);
        vm.expectRevert(FloatPool.NotPendingAdmin.selector);
        pool.acceptAdmin();
        vm.expectRevert(JobVault.NotPendingAdmin.selector);
        vault.acceptAdmin();
        vm.stopPrank();
    }

    // ── 3. Review findings: lockout, mixed changes, timers ──────────────

    function test_acceptBeforeTheDelayReverts() public {
        vm.startPrank(ADMIN);
        pool.transferAdmin(NEXT);
        vault.transferAdmin(NEXT);
        vm.stopPrank();
        vm.warp(block.timestamp + pool.ADMIN_TRANSFER_DELAY() - 1);

        vm.startPrank(NEXT);
        vm.expectRevert(FloatPool.AdminTransferTimelocked.selector);
        pool.acceptAdmin();
        vm.expectRevert(JobVault.AdminTransferTimelocked.selector);
        vault.acceptAdmin();
        vm.stopPrank();
    }

    /// The lockout the delay exists for. An attacker holding the same key starts a rotation to
    /// an address it controls; the real holder sees AdminTransferStarted and cancels it inside
    /// the window, keeping pause, lower and cancel. Without the delay, the attacker's accept
    /// would land first and the holder would be out for good.
    function test_aLeakedKeyCannotRotateTheHolderOut() public {
        vm.prank(ADMIN); // the attacker, using the leaked key
        pool.transferAdmin(ATTACKER);
        vm.prank(ADMIN); // the attacker, same key
        vault.transferAdmin(ATTACKER);

        vm.prank(ATTACKER);
        vm.expectRevert(FloatPool.AdminTransferTimelocked.selector);
        pool.acceptAdmin();

        vm.startPrank(ADMIN); // the holder, inside the window
        pool.transferAdmin(ADMIN);
        vault.transferAdmin(ADMIN);
        vm.stopPrank();

        vm.warp(block.timestamp + pool.ADMIN_TRANSFER_DELAY());
        vm.startPrank(ATTACKER);
        vm.expectRevert(FloatPool.NotPendingAdmin.selector);
        pool.acceptAdmin();
        vm.expectRevert(JobVault.NotPendingAdmin.selector);
        vault.acceptAdmin();
        vm.stopPrank();

        assertEq(pool.admin(), ADMIN);
        assertEq(vault.admin(), ADMIN);
        vm.prank(ADMIN);
        pool.setPaused(true); // the holder still has the emergency stop
    }

    function test_aSecondTransferReplacesThePendingAdminAndRestartsTheDelay() public {
        vm.prank(ADMIN);
        pool.transferAdmin(NEXT);
        vm.warp(block.timestamp + 1 days);
        vm.prank(ADMIN);
        pool.transferAdmin(ATTACKER);
        assertEq(pool.pendingAdmin(), ATTACKER);
        assertEq(pool.adminTransferAcceptableAt(), uint64(block.timestamp + pool.ADMIN_TRANSFER_DELAY()));

        vm.warp(block.timestamp + 1 days); // NEXT's original window would have elapsed here
        vm.prank(NEXT);
        vm.expectRevert(FloatPool.NotPendingAdmin.selector);
        pool.acceptAdmin();
        vm.prank(ATTACKER);
        vm.expectRevert(FloatPool.AdminTransferTimelocked.selector);
        pool.acceptAdmin();
    }

    /// Raising one cap while lowering the other used to queue the whole change, silently
    /// deferring the lowering by the raise delay. In an incident that is a trap; it reverts.
    function test_aMixedRaiseAndLowerReverts() public {
        vm.startPrank(ADMIN);
        vm.expectRevert(FloatPool.MixedCapChange.selector);
        pool.setCaps(EXPOSURE + 1, ADVANCE - 1);
        vm.expectRevert(FloatPool.MixedCapChange.selector);
        pool.setCaps(EXPOSURE - 1, ADVANCE + 1);
        vm.stopPrank();
        assertEq(pool.maxTotalExposure(), EXPOSURE);
        assertEq(pool.capRaiseExecutableAt(), 0);
    }

    function test_reQueueingARaiseRestartsTheTimer() public {
        vm.prank(ADMIN);
        pool.setCaps(EXPOSURE * 2, ADVANCE);
        vm.warp(block.timestamp + 1 days);
        vm.prank(ADMIN);
        pool.setCaps(EXPOSURE * 3, ADVANCE);
        assertEq(pool.capRaiseExecutableAt(), uint64(block.timestamp + pool.CAP_RAISE_DELAY()));

        vm.warp(block.timestamp + 1 days); // the first raise's delay would be over
        vm.prank(ADMIN);
        vm.expectRevert(FloatPool.CapRaiseTimelocked.selector);
        pool.executeCapsRaise();
    }
}
