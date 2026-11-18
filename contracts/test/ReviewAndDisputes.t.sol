// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

/// Internal review, Nov 2026 (docs/INTERNAL-REVIEW-2026-11.md).
///
/// F1: a customer holding the deliverable could wait out the deadline and refund in full.
/// F2: the JobVault admin could refund, and so write off, any live job at any time.
/// F4: a pool whose LP assets were all written off divided by zero on every deposit.
///
/// A delivery now opens REVIEW_PERIOD: the customer accepts or disputes, silence lets the
/// operator finalize, and a dispute is the only way the admin can end a delivered job.
contract ReviewAndDisputesTest is Test {
    JobVault internal vault;
    FloatPool internal pool;
    MockUSDC internal usdc;

    address internal constant ADMIN = address(0xA0);
    address internal constant OPERATOR = address(0x09);
    address internal constant CUSTOMER = address(0xC0);
    address internal constant STRANGER = address(0xBAD);
    address internal constant LP = address(0x1D);
    address internal constant LP2 = address(0x1D2);

    uint256 internal constant PAYMENT = 25_000_000;   // 25.00
    uint256 internal constant PRINCIPAL = 12_500_000; // 50% base rate
    uint256 internal constant OWED = 12_750_000;      // + 2% fee

    bytes32 internal constant JOB = keccak256("review-job");

    event DeliveryDisputed(bytes32 indexed jobId);
    event DisputeResolved(bytes32 indexed jobId, bool refunded);
    event JobSettled(bytes32 indexed jobId, uint256 advanceRepaid, uint256 operatorNet);

    function setUp() public {
        usdc = new MockUSDC();
        vm.startPrank(ADMIN);
        vault = new JobVault(IERC20(address(usdc)));
        pool = new FloatPool(IERC20(address(usdc)));
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));
        vault.setMaxJobPayment(type(uint256).max);
        pool.setOperatorAllowed(OPERATOR, true); // before the first setCaps: immediate
        pool.setCaps(type(uint256).max, type(uint256).max);
        pool.setDepositorAllowed(LP, true);
        pool.setDepositorAllowed(LP2, true);
        vm.stopPrank();

        _deposit(LP, 150_000_000);
        usdc.mint(CUSTOMER, 10 * PAYMENT);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
    }

    // ── helpers ──────────────────────────────────────────────────────────

    function _deposit(address lp, uint256 amount) internal {
        usdc.mint(lp, amount);
        vm.startPrank(lp);
        usdc.approve(address(pool), type(uint256).max);
        pool.deposit(amount, lp);
        vm.stopPrank();
    }

    function _funded(bytes32 id, bool withAdvance) internal {
        vm.prank(OPERATOR);
        vault.createJob(id, CUSTOMER, OPERATOR, PAYMENT, 0, keccak256("terms"), uint64(block.timestamp + 1 days));
        vm.prank(CUSTOMER);
        vault.fund(id);
        if (withAdvance) {
            vm.prank(OPERATOR);
            pool.requestAdvance(id);
        }
    }

    function _delivered(bytes32 id, bool withAdvance) internal {
        _funded(id, withAdvance);
        vm.startPrank(OPERATOR);
        vault.startWork(id);
        vault.submitDelivery(id, keccak256("deliverable"));
        vm.stopPrank();
    }

    function _status(bytes32 id) internal view returns (JobVault.JobStatus) {
        return vault.jobStatus(id);
    }

    // ── F1: no timeout refund after delivery ─────────────────────────────

    function test_F1_customerCannotRefundADeliveredJobAfterTheDeadline() public {
        _delivered(JOB, true);
        vm.warp(block.timestamp + 30 days);

        vm.prank(CUSTOMER);
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vault.refund(JOB);

        (, , bool open) = pool.openAdvanceOf(JOB);
        assertTrue(open, "no write-off");
        assertEq(pool.writtenOffJobs(OPERATOR), 0);
    }

    function test_customerTimeoutRefund_stillWorksForUndeliveredWork() public {
        _funded(JOB, true);
        vm.prank(OPERATOR);
        vault.startWork(JOB);
        vm.warp(block.timestamp + 1 days);

        uint256 before = usdc.balanceOf(CUSTOMER);
        vm.prank(CUSTOMER);
        vault.refund(JOB);
        assertEq(usdc.balanceOf(CUSTOMER) - before, PAYMENT);
        assertEq(pool.totalWrittenOff(), PRINCIPAL);
    }

    function test_submitDelivery_recordsDeliveredAt() public {
        _delivered(JOB, false);
        assertEq(vault.deliveredAt(JOB), block.timestamp);
    }

    // ── finalizeDelivery ─────────────────────────────────────────────────

    function test_finalize_revertsInsideTheReviewPeriod() public {
        _delivered(JOB, true);
        vm.warp(block.timestamp + vault.REVIEW_PERIOD() - 1);
        vm.prank(OPERATOR);
        vm.expectRevert(JobVault.ReviewPeriodOpen.selector);
        vault.finalizeDelivery(JOB);
    }

    function test_finalize_settlesThroughTheWaterfallAfterSilence() public {
        _delivered(JOB, true);
        vm.warp(block.timestamp + vault.REVIEW_PERIOD());

        uint256 opBefore = usdc.balanceOf(OPERATOR);
        vm.expectEmit(true, false, false, true, address(vault));
        emit JobSettled(JOB, OWED, PAYMENT - OWED);
        vm.prank(OPERATOR);
        vault.finalizeDelivery(JOB);

        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Accepted));
        assertEq(usdc.balanceOf(OPERATOR) - opBefore, PAYMENT - OWED);
        (, , bool open) = pool.openAdvanceOf(JOB);
        assertFalse(open, "pool repaid");
        assertEq(pool.acceptedJobs(OPERATOR), 1);
    }

    function test_finalize_isOperatorOnly() public {
        _delivered(JOB, false);
        vm.warp(block.timestamp + vault.REVIEW_PERIOD());
        vm.prank(STRANGER);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.finalizeDelivery(JOB);
        vm.prank(ADMIN);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.finalizeDelivery(JOB);
    }

    function test_finalize_revertsBeforeDelivery() public {
        _funded(JOB, false);
        vm.prank(OPERATOR);
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vault.finalizeDelivery(JOB);
    }

    function test_finalize_isBlockedByADispute() public {
        _delivered(JOB, false);
        vm.prank(CUSTOMER);
        vault.dispute(JOB);
        vm.warp(block.timestamp + 30 days);
        vm.prank(OPERATOR);
        vm.expectRevert(JobVault.AlreadyDisputed.selector);
        vault.finalizeDelivery(JOB);
    }

    // ── dispute ──────────────────────────────────────────────────────────

    function test_dispute_marksTheJobAndEmits() public {
        _delivered(JOB, false);
        vm.expectEmit(true, false, false, false, address(vault));
        emit DeliveryDisputed(JOB);
        vm.prank(CUSTOMER);
        vault.dispute(JOB);
        assertTrue(vault.disputed(JOB));
        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Delivered));
    }

    function test_dispute_isCustomerOnly() public {
        _delivered(JOB, false);
        vm.prank(OPERATOR);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.dispute(JOB);
        vm.prank(ADMIN);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.dispute(JOB);
    }

    function test_dispute_closesWithTheReviewPeriod() public {
        _delivered(JOB, false);
        vm.warp(block.timestamp + vault.REVIEW_PERIOD());
        vm.prank(CUSTOMER);
        vm.expectRevert(JobVault.ReviewPeriodOver.selector);
        vault.dispute(JOB);
    }

    function test_dispute_onlyOnce() public {
        _delivered(JOB, false);
        vm.startPrank(CUSTOMER);
        vault.dispute(JOB);
        vm.expectRevert(JobVault.AlreadyDisputed.selector);
        vault.dispute(JOB);
        vm.stopPrank();
    }

    function test_dispute_revertsBeforeDelivery() public {
        _funded(JOB, false);
        vm.prank(CUSTOMER);
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vault.dispute(JOB);
    }

    function test_customerCanStillAcceptAfterDisputing() public {
        _delivered(JOB, true);
        vm.startPrank(CUSTOMER);
        vault.dispute(JOB);
        vault.acceptDelivery(JOB);
        vm.stopPrank();
        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Accepted));
    }

    function test_operatorCanConcedeADisputeByRefunding() public {
        _delivered(JOB, true);
        vm.prank(CUSTOMER);
        vault.dispute(JOB);
        uint256 before = usdc.balanceOf(CUSTOMER);
        vm.prank(OPERATOR);
        vault.refund(JOB);
        assertEq(usdc.balanceOf(CUSTOMER) - before, PAYMENT);
    }

    // ── resolveDispute ───────────────────────────────────────────────────

    function test_resolve_refundMakesTheCustomerWholeAndWritesOff() public {
        _delivered(JOB, true);
        vm.prank(CUSTOMER);
        vault.dispute(JOB);

        uint256 before = usdc.balanceOf(CUSTOMER);
        vm.expectEmit(true, false, false, true, address(vault));
        emit DisputeResolved(JOB, true);
        vm.prank(ADMIN);
        vault.resolveDispute(JOB, true);

        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Refunded));
        assertEq(usdc.balanceOf(CUSTOMER) - before, PAYMENT);
        assertEq(pool.totalWrittenOff(), PRINCIPAL);
    }

    function test_resolve_settleRunsTheWaterfall() public {
        _delivered(JOB, true);
        vm.prank(CUSTOMER);
        vault.dispute(JOB);

        uint256 opBefore = usdc.balanceOf(OPERATOR);
        vm.prank(ADMIN);
        vault.resolveDispute(JOB, false);

        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Accepted));
        assertEq(usdc.balanceOf(OPERATOR) - opBefore, PAYMENT - OWED);
        assertEq(pool.acceptedJobs(OPERATOR), 1);
    }

    function test_resolve_isAdminOnly() public {
        _delivered(JOB, false);
        vm.prank(CUSTOMER);
        vault.dispute(JOB);
        vm.prank(CUSTOMER);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.resolveDispute(JOB, true);
        vm.prank(OPERATOR);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.resolveDispute(JOB, false);
    }

    function test_resolve_needsADispute() public {
        _delivered(JOB, true);
        vm.prank(ADMIN);
        vm.expectRevert(JobVault.NotDisputed.selector);
        vault.resolveDispute(JOB, true);
    }

    function test_resolve_cannotRunTwice() public {
        _delivered(JOB, false);
        vm.prank(CUSTOMER);
        vault.dispute(JOB);
        vm.startPrank(ADMIN);
        vault.resolveDispute(JOB, true);
        vm.expectRevert(JobVault.NotDisputed.selector);
        vault.resolveDispute(JOB, false);
        vm.stopPrank();
    }

    function test_disputeAndResolve_workWhilePaused() public {
        _delivered(JOB, true);
        vm.startPrank(ADMIN);
        vault.setPaused(true);
        pool.setPaused(true);
        vm.stopPrank();

        vm.prank(CUSTOMER);
        vault.dispute(JOB);
        vm.prank(ADMIN);
        vault.resolveDispute(JOB, true);
        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Refunded));
    }

    // ── F2: the admin cannot write off the book on its own ───────────────

    function test_F2_adminCannotRefundALiveJobBeforeItsDeadline() public {
        _funded(JOB, true);
        vm.prank(ADMIN);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.refund(JOB);
    }

    function test_F2_adminCannotRefundADeliveredJobEvenAfterTheDeadline() public {
        _delivered(JOB, true);
        vm.warp(block.timestamp + 30 days);
        vm.prank(ADMIN);
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vault.refund(JOB);
    }

    function test_adminCanRefundUndeliveredWorkPastTheDeadline() public {
        _funded(JOB, true);
        vm.warp(block.timestamp + 1 days);
        uint256 before = usdc.balanceOf(CUSTOMER);
        vm.prank(ADMIN);
        vault.refund(JOB);
        assertEq(usdc.balanceOf(CUSTOMER) - before, PAYMENT, "the refund still goes to the customer");
    }

    // ── F4: a wiped-out pool says so instead of dividing by zero ─────────

    function _wipeOut() internal {
        // 10% org cap: a 12.50 advance needs >= 125.00 of LP assets; the seed is 150.00.
        _funded(JOB, true);
        uint256 idle = pool.totalAssets() - pool.totalOutstanding();
        vm.prank(LP);
        pool.withdraw(idle, LP, LP);
        vm.prank(OPERATOR);
        vault.refund(JOB);
        assertEq(pool.totalAssets(), 0);
        assertGt(pool.totalShares(), 0);
    }

    function test_F4_depositIntoAWipedOutPoolRevertsExplicitly() public {
        _wipeOut();
        usdc.mint(LP2, 10_000_000);
        vm.startPrank(LP2);
        usdc.approve(address(pool), type(uint256).max);
        vm.expectRevert(FloatPool.PoolWipedOut.selector);
        pool.deposit(10_000_000, LP2);
        vm.stopPrank();
    }

    function test_F4_withdrawFromAWipedOutPoolRevertsExplicitly() public {
        _wipeOut();
        vm.prank(LP);
        vm.expectRevert(FloatPool.InsufficientLiquidity.selector);
        pool.withdraw(1, LP, LP);
    }
}
