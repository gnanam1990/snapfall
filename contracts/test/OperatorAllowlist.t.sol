// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

/// Operator allowlist (Nov 2026, security audit).
///
/// JobVault.createJob lets any address register a job that names itself as the operator, and
/// the same address may name itself as the customer. Before the allowlist, requestAdvance
/// authorised a borrower only by `msg.sender == the vault-reported operator`, so an outsider
/// could create -> fund -> requestAdvance -> refund: the escrow came back in full, the advance
/// stayed with the outsider, and the pool wrote it off against the LPs. A fresh address per
/// cycle sidestepped the per-org cap and the write-off penalty, so the loop ran until
/// maxTotalExposure was spent.
///
/// These tests pin the fix: borrowing is an admin grant held by FloatPool, it fails closed,
/// a grant on a configured pool waits OPERATOR_GRANT_DELAY, and a revocation is immediate.
contract OperatorAllowlistTest is Test {
    JobVault internal vault;
    FloatPool internal pool;
    MockUSDC internal usdc;

    address internal constant ADMIN    = address(0xA0);
    address internal constant CUSTOMER = address(0xC0);
    address internal constant OPERATOR = address(0x09);
    address internal constant NEW_ORG  = address(0x0A);
    address internal constant STRANGER = address(0xBAD);
    address internal constant LP       = address(0x1D);

    bytes32 internal constant TERMS = keccak256("terms");
    bytes32 internal constant DELIV = keccak256("deliverable");

    // The documented example configuration (docs/MAINNET.md "Choosing the numbers").
    uint256 internal constant MAX_EXPOSURE = 50_000_000; // 50.00 USDC
    uint256 internal constant MAX_ADVANCE  = 10_000_000; // 10.00 USDC
    uint256 internal constant MAX_JOB      = 25_000_000; // 25.00 USDC
    uint256 internal constant POOL_SEED    = 150_000_000;
    uint256 internal constant PAYMENT      = 20_000_000; // 50% of it is exactly MAX_ADVANCE

    event OperatorAllowed(address indexed operator, bool allowed);
    event OperatorGrantQueued(address indexed operator, uint64 executableAt);
    event OperatorGrantCancelled(address indexed operator);

    function setUp() public {
        usdc = new MockUSDC();

        vm.startPrank(ADMIN);
        vault = new JobVault(IERC20(address(usdc)));
        pool = new FloatPool(IERC20(address(usdc)));
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));
        pool.setOperatorAllowed(OPERATOR, true); // before the first setCaps: immediate
        pool.setCaps(MAX_EXPOSURE, MAX_ADVANCE);
        vault.setMaxJobPayment(MAX_JOB);
        pool.setDepositorAllowed(LP, true);
        vm.stopPrank();

        usdc.mint(LP, POOL_SEED);
        vm.startPrank(LP);
        usdc.approve(address(pool), type(uint256).max);
        pool.deposit(POOL_SEED, LP);
        vm.stopPrank();

        usdc.mint(CUSTOMER, PAYMENT * 10);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
    }

    // ── helpers ──────────────────────────────────────────────────────────

    function _createAndFund(bytes32 jobId, address operator) internal {
        vm.prank(operator); // self-service: the operator registers its own job
        vault.createJob(jobId, CUSTOMER, operator, PAYMENT, 0, TERMS, uint64(block.timestamp + 7 days));
        vm.prank(CUSTOMER);
        vault.fund(jobId);
    }

    /// Two 10.00 draws for one org need 200.00 of TVL under the 10%-per-org cap.
    function _deepenPoolForTwoDraws() internal {
        usdc.mint(LP, 50_000_000);
        vm.prank(LP);
        pool.deposit(50_000_000, LP);
    }

    /// One outsider cycle up to the advance: create (as both customer and operator) and fund.
    function _outsiderJob(address attacker, bytes32 jobId) internal {
        usdc.mint(attacker, PAYMENT);
        vm.startPrank(attacker);
        usdc.approve(address(vault), type(uint256).max);
        vault.createJob(jobId, attacker, attacker, PAYMENT, 0, TERMS, uint64(block.timestamp + 1 days));
        vault.fund(jobId);
        vm.stopPrank();
    }

    // ── 1. The outsider drain is closed ──────────────────────────────────

    /// The audit's proof of concept, run against the fixed contracts: fresh addresses with no
    /// privileges each try create -> fund -> requestAdvance -> refund. Every draw reverts, every
    /// attacker ends exactly where it started, and the pool has lost nothing.
    function test_outsiderDrain_freshAddressesCannotBorrow() public {
        uint256 assetsBefore = pool.totalAssets();

        for (uint256 i = 1; i <= 10; i++) {
            address attacker = address(uint160(0xBEEF00 + i));
            bytes32 jobId = keccak256(abi.encode("atk", i));
            _outsiderJob(attacker, jobId);

            vm.expectRevert(FloatPool.OperatorNotAllowed.selector);
            vm.prank(attacker);
            pool.requestAdvance(jobId);

            vm.prank(attacker);
            vault.refund(jobId); // the escrow comes back; there is no advance to write off

            assertEq(usdc.balanceOf(attacker), PAYMENT, "attacker gains nothing");
        }

        assertEq(pool.totalAssets(), assetsBefore, "LP capital untouched");
        assertEq(pool.totalOutstanding(), 0, "nothing was lent");
        assertEq(pool.totalWrittenOff(), 0, "nothing was lost");
        assertEq(usdc.balanceOf(address(pool)), assetsBefore, "pool cash untouched");

        // And the loss budget is still whole for the real operator.
        _createAndFund(keccak256("honest"), OPERATOR);
        vm.prank(OPERATOR);
        assertEq(pool.requestAdvance(keccak256("honest")), MAX_ADVANCE, "real operator still borrows");
    }

    /// Any address the admin has not granted is refused, whoever it is and whatever job it holds.
    function testFuzz_unallowedOperatorCannotBorrow(address attacker) public {
        vm.assume(attacker != address(0) && attacker != OPERATOR);
        vm.assume(attacker != address(vault) && attacker != address(pool) && attacker != LP);
        bytes32 jobId = keccak256(abi.encode("fuzz", attacker));
        _outsiderJob(attacker, jobId);

        vm.expectRevert(FloatPool.OperatorNotAllowed.selector);
        vm.prank(attacker);
        pool.requestAdvance(jobId);
    }

    /// Admin authority over the vault is not borrowing authority. A job the admin registers
    /// naming itself as operator still cannot draw until the pool's own list says so.
    function test_adminIsNotImplicitlyAnOperator() public {
        bytes32 jobId = keccak256("admin-job");
        vm.prank(ADMIN);
        vault.createJob(jobId, CUSTOMER, ADMIN, PAYMENT, 0, TERMS, uint64(block.timestamp + 7 days));
        vm.prank(CUSTOMER);
        vault.fund(jobId);

        vm.expectRevert(FloatPool.OperatorNotAllowed.selector);
        vm.prank(ADMIN);
        pool.requestAdvance(jobId);
    }

    /// The identity check still comes first: an allowed operator's job cannot be drawn by anyone
    /// else, and a stranger is told NotTreasury, not OperatorNotAllowed.
    function test_jobOperatorCheckStillPrecedesAllowlist() public {
        _createAndFund(keccak256("j"), OPERATOR);
        vm.expectRevert(FloatPool.NotTreasury.selector);
        vm.prank(STRANGER);
        pool.requestAdvance(keccak256("j"));
    }

    // ── 2. Fail closed, and the allowed path still works ─────────────────

    function test_freshPoolAllowsNobody() public {
        vm.prank(ADMIN);
        FloatPool fresh = new FloatPool(IERC20(address(usdc)));
        assertFalse(fresh.allowedOperator(ADMIN), "not even the deployer");
        assertFalse(fresh.allowedOperator(OPERATOR));
    }

    function test_allowedOperator_drawsAndSettles() public {
        assertTrue(pool.allowedOperator(OPERATOR));
        bytes32 jobId = keccak256("happy");
        _createAndFund(jobId, OPERATOR);

        vm.prank(OPERATOR);
        uint256 amount = pool.requestAdvance(jobId);
        assertEq(amount, MAX_ADVANCE, "50% of 20.00");
        assertEq(usdc.balanceOf(OPERATOR), MAX_ADVANCE, "treasury credited");

        vm.startPrank(OPERATOR);
        vault.startWork(jobId);
        vault.submitDelivery(jobId, DELIV);
        vm.stopPrank();
        vm.prank(CUSTOMER);
        vault.acceptDelivery(jobId);

        (, , bool open) = pool.openAdvanceOf(jobId);
        assertFalse(open, "repaid through the waterfall");
        assertEq(pool.totalOutstanding(), 0);
    }

    function test_grantBeforeFirstConfigurationIsImmediate() public {
        vm.startPrank(ADMIN);
        FloatPool fresh = new FloatPool(IERC20(address(usdc)));
        vm.expectEmit(address(fresh));
        emit OperatorAllowed(OPERATOR, true);
        fresh.setOperatorAllowed(OPERATOR, true);
        vm.stopPrank();

        assertTrue(fresh.allowedOperator(OPERATOR));
        assertEq(fresh.operatorGrantExecutableAt(OPERATOR), 0, "nothing queued");
    }

    // ── 3. A grant on a configured pool is delayed ───────────────────────

    function test_grantAfterConfigurationIsQueuedNotApplied() public {
        uint64 at = uint64(block.timestamp + pool.OPERATOR_GRANT_DELAY());
        vm.expectEmit(address(pool));
        emit OperatorGrantQueued(NEW_ORG, at);
        vm.prank(ADMIN);
        pool.setOperatorAllowed(NEW_ORG, true);

        assertFalse(pool.allowedOperator(NEW_ORG), "a grant must not take effect immediately");
        assertEq(pool.operatorGrantExecutableAt(NEW_ORG), at);

        // The queued org cannot borrow in the window.
        _createAndFund(keccak256("early"), NEW_ORG);
        vm.expectRevert(FloatPool.OperatorNotAllowed.selector);
        vm.prank(NEW_ORG);
        pool.requestAdvance(keccak256("early"));
    }

    function test_grantDelayMatchesTheCapRaiseDelay() public view {
        assertEq(pool.OPERATOR_GRANT_DELAY(), 2 days);
        assertEq(pool.OPERATOR_GRANT_DELAY(), pool.CAP_RAISE_DELAY());
    }

    function test_grantCannotExecuteBeforeTheDelay() public {
        vm.startPrank(ADMIN);
        pool.setOperatorAllowed(NEW_ORG, true);
        vm.warp(block.timestamp + pool.OPERATOR_GRANT_DELAY() - 1);
        vm.expectRevert(FloatPool.OperatorGrantTimelocked.selector);
        pool.executeOperatorGrant(NEW_ORG);
        vm.stopPrank();
    }

    function test_grantExecutesAfterTheDelay() public {
        vm.prank(ADMIN);
        pool.setOperatorAllowed(NEW_ORG, true);
        vm.warp(block.timestamp + pool.OPERATOR_GRANT_DELAY());

        vm.expectEmit(address(pool));
        emit OperatorAllowed(NEW_ORG, true);
        vm.prank(ADMIN);
        pool.executeOperatorGrant(NEW_ORG);

        assertTrue(pool.allowedOperator(NEW_ORG));
        assertEq(pool.operatorGrantExecutableAt(NEW_ORG), 0, "consumed");

        _createAndFund(keccak256("later"), NEW_ORG);
        vm.prank(NEW_ORG);
        assertEq(pool.requestAdvance(keccak256("later")), MAX_ADVANCE);
    }

    function test_cancelDropsAQueuedGrant() public {
        vm.startPrank(ADMIN);
        pool.setOperatorAllowed(NEW_ORG, true);
        vm.expectEmit(address(pool));
        emit OperatorGrantCancelled(NEW_ORG);
        pool.cancelOperatorGrant(NEW_ORG);

        vm.warp(block.timestamp + pool.OPERATOR_GRANT_DELAY());
        vm.expectRevert(FloatPool.NoOperatorGrantQueued.selector);
        pool.executeOperatorGrant(NEW_ORG);
        vm.stopPrank();
        assertFalse(pool.allowedOperator(NEW_ORG));
    }

    function test_cancelAndExecuteRevertWithNothingQueued() public {
        vm.startPrank(ADMIN);
        vm.expectRevert(FloatPool.NoOperatorGrantQueued.selector);
        pool.cancelOperatorGrant(NEW_ORG);
        vm.expectRevert(FloatPool.NoOperatorGrantQueued.selector);
        pool.executeOperatorGrant(NEW_ORG);
        vm.stopPrank();
    }

    /// Re-granting an operator that is already allowed is not a risk increase and does not queue.
    function test_regrantingAnAllowedOperatorDoesNotQueue() public {
        vm.prank(ADMIN);
        pool.setOperatorAllowed(OPERATOR, true);
        assertTrue(pool.allowedOperator(OPERATOR));
        assertEq(pool.operatorGrantExecutableAt(OPERATOR), 0);
    }

    // ── 4. Revocation is immediate ───────────────────────────────────────

    function test_revocationTakesEffectImmediately() public {
        bytes32 first = keccak256("first");
        bytes32 second = keccak256("second");
        _createAndFund(first, OPERATOR);
        _createAndFund(second, OPERATOR);
        _deepenPoolForTwoDraws(); // so the second draw would pass every other check

        vm.prank(OPERATOR);
        pool.requestAdvance(first);

        vm.expectEmit(address(pool));
        emit OperatorAllowed(OPERATOR, false);
        vm.prank(ADMIN);
        pool.setOperatorAllowed(OPERATOR, false); // configured pool, yet no delay

        assertFalse(pool.allowedOperator(OPERATOR));
        vm.expectRevert(FloatPool.OperatorNotAllowed.selector);
        vm.prank(OPERATOR);
        pool.requestAdvance(second);
    }

    /// Revoking does not strand an advance that is already out: it still closes through its job.
    function test_revocationLeavesOpenAdvancesSettleable() public {
        bytes32 settled = keccak256("settled");
        bytes32 refunded = keccak256("refunded");
        _createAndFund(settled, OPERATOR);
        _createAndFund(refunded, OPERATOR);
        _deepenPoolForTwoDraws();
        vm.startPrank(OPERATOR);
        pool.requestAdvance(settled);
        pool.requestAdvance(refunded);
        vm.stopPrank();

        vm.prank(ADMIN);
        pool.setOperatorAllowed(OPERATOR, false);

        vm.startPrank(OPERATOR);
        vault.startWork(settled);
        vault.submitDelivery(settled, DELIV);
        vm.stopPrank();
        vm.prank(CUSTOMER);
        vault.acceptDelivery(settled); // repayAdvance still runs

        vm.prank(OPERATOR);
        vault.refund(refunded); // writeOff still runs

        assertEq(pool.totalOutstanding(), 0, "both advances closed");
        assertEq(pool.totalWrittenOff(), MAX_ADVANCE);
    }

    function test_revocationDropsAQueuedGrant() public {
        vm.startPrank(ADMIN);
        pool.setOperatorAllowed(NEW_ORG, true);

        vm.expectEmit(address(pool));
        emit OperatorGrantCancelled(NEW_ORG);
        vm.expectEmit(address(pool));
        emit OperatorAllowed(NEW_ORG, false);
        pool.setOperatorAllowed(NEW_ORG, false);

        vm.warp(block.timestamp + pool.OPERATOR_GRANT_DELAY());
        vm.expectRevert(FloatPool.NoOperatorGrantQueued.selector);
        pool.executeOperatorGrant(NEW_ORG);
        vm.stopPrank();
        assertFalse(pool.allowedOperator(NEW_ORG));
    }

    // ── 5. Only the admin manages the list ───────────────────────────────

    function test_onlyAdminCanChangeTheList() public {
        vm.prank(ADMIN);
        pool.setOperatorAllowed(NEW_ORG, true); // queue one so execute/cancel have a target
        vm.warp(block.timestamp + pool.OPERATOR_GRANT_DELAY());

        address[3] memory callers = [STRANGER, OPERATOR, LP];
        for (uint256 i = 0; i < callers.length; i++) {
            vm.startPrank(callers[i]);
            vm.expectRevert(FloatPool.NotAuthorized.selector);
            pool.setOperatorAllowed(callers[i], true);
            vm.expectRevert(FloatPool.NotAuthorized.selector);
            pool.setOperatorAllowed(OPERATOR, false);
            vm.expectRevert(FloatPool.NotAuthorized.selector);
            pool.executeOperatorGrant(NEW_ORG);
            vm.expectRevert(FloatPool.NotAuthorized.selector);
            pool.cancelOperatorGrant(NEW_ORG);
            vm.stopPrank();
        }

        assertTrue(pool.allowedOperator(OPERATOR), "unchanged");
        assertFalse(pool.allowedOperator(NEW_ORG), "unchanged");
        assertGt(pool.operatorGrantExecutableAt(NEW_ORG), 0, "still queued");
    }

    function test_zeroAddressIsRejected() public {
        vm.startPrank(ADMIN);
        vm.expectRevert(FloatPool.ZeroAddress.selector);
        pool.setOperatorAllowed(address(0), true);
        vm.expectRevert(FloatPool.ZeroAddress.selector);
        pool.setOperatorAllowed(address(0), false);
        vm.stopPrank();
    }

    // ── 6. A queued grant across an admin handover ───────────────────────
    //
    // Decided, not overlooked: acceptAdmin does NOT clear queued grants, exactly as it does not
    // clear a queued cap raise. Neither applies itself: executeOperatorGrant is admin-only, so
    // after the handover only the NEW admin can apply a grant the old key queued, and it can
    // cancel it instead. Clearing per-address mappings on handover would need an epoch scheme for
    // no safety gain. docs/MAINNET.md tells the new admin to review OperatorGrantQueued.

    function test_queuedGrantSurvivesHandoverButOnlyTheNewAdminCanActOnIt() public {
        address newAdmin = address(0xAD);
        vm.startPrank(ADMIN);
        pool.setOperatorAllowed(NEW_ORG, true); // queued by the outgoing key
        pool.transferAdmin(newAdmin);
        vm.stopPrank();

        vm.warp(block.timestamp + pool.ADMIN_TRANSFER_DELAY()); // also >= OPERATOR_GRANT_DELAY
        vm.prank(newAdmin);
        pool.acceptAdmin();

        assertGt(pool.operatorGrantExecutableAt(NEW_ORG), 0, "still queued after handover");
        assertFalse(pool.allowedOperator(NEW_ORG), "and still not applied");

        // The outgoing key has lost the power to apply it.
        vm.prank(ADMIN);
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.executeOperatorGrant(NEW_ORG);

        // The new admin decides: here it cancels, and the grant is gone for good.
        vm.prank(newAdmin);
        pool.cancelOperatorGrant(NEW_ORG);
        vm.prank(newAdmin);
        vm.expectRevert(FloatPool.NoOperatorGrantQueued.selector);
        pool.executeOperatorGrant(NEW_ORG);
        assertFalse(pool.allowedOperator(NEW_ORG));
    }
}
