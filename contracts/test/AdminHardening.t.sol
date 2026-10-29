// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
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
///   2. admin moves only by transferAdmin + acceptAdmin, on both contracts.
contract AdminHardeningTest is Test {
    JobVault internal vault;
    FloatPool internal pool;
    MockUSDC internal usdc;

    address internal constant ADMIN    = address(0xA0);
    address internal constant NEXT     = address(0xA1);
    address internal constant STRANGER = address(0xBAD);
    address internal constant LP       = address(0x1D);

    uint256 internal constant EXPOSURE = 50_000_000;
    uint256 internal constant ADVANCE  = 10_000_000;

    event CapsSet(uint256 maxTotalExposure, uint256 maxAdvance);
    event CapsRaiseQueued(uint256 maxTotalExposure, uint256 maxAdvance, uint64 executableAt);
    event CapsRaiseCancelled();
    event AdminTransferStarted(address indexed currentAdmin, address indexed pendingAdmin);
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
        vm.expectEmit(address(pool));
        emit CapsSet(EXPOSURE * 10, ADVANCE * 2);
        pool.executeCapsRaise();
        vm.stopPrank();

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
        vm.expectEmit(address(pool));
        emit AdminTransferStarted(ADMIN, NEXT);
        vm.prank(ADMIN);
        pool.transferAdmin(NEXT);
        assertEq(pool.admin(), ADMIN, "nothing moves until the new admin accepts");

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
        pool.transferAdmin(ADMIN);
        vault.transferAdmin(NEXT);
        vault.transferAdmin(ADMIN);
        vm.stopPrank();
        assertEq(pool.pendingAdmin(), address(0));
        assertEq(vault.pendingAdmin(), address(0));

        vm.startPrank(NEXT);
        vm.expectRevert(FloatPool.NotPendingAdmin.selector);
        pool.acceptAdmin();
        vm.expectRevert(JobVault.NotPendingAdmin.selector);
        vault.acceptAdmin();
        vm.stopPrank();
    }
}
