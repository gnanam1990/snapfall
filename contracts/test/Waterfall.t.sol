// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";
import {FloatPoolHarness} from "./mocks/FloatPoolHarness.sol";

/// SC-JV-005/009 settlement waterfall + SC-FP-008 loss waterfall.
///
/// "The fall" — the 2:10 demo beat. Strict seniority in one transaction:
/// pool principal + fee first, operator last.
contract WaterfallTest is Test {
    JobVault internal vault;
    FloatPoolHarness internal pool;
    MockUSDC internal usdc;

    address internal constant ADMIN    = address(0xA0);
    address internal constant CUSTOMER = address(0xC0);
    address internal constant OPERATOR = address(0x09);
    address internal constant STRANGER = address(0xBAD);
    address internal constant LP       = address(0x1D);

    bytes32 internal constant JOB   = keccak256("job_104");
    bytes32 internal constant TERMS = keccak256("competitor analysis");
    bytes32 internal constant DELIV = keccak256("competitor-analysis.pdf");

    uint256 internal constant PAYMENT   = 25_000_000;  // 25.00
    uint256 internal constant BUDGET    =  6_000_000;  //  6.00
    uint256 internal constant POOL_SEED = 150_000_000; // 150.00 (see SPEC-06)

    uint256 internal constant PRINCIPAL = 12_500_000;  // 12.50
    uint256 internal constant FEE       =    250_000;  //  0.25
    uint256 internal constant OWED      = 12_750_000;  // 12.75
    uint256 internal constant OPNET     = 12_250_000;  // 12.25

    uint64 internal deadline;

    event JobSettled(bytes32 indexed jobId, uint256 advanceRepaid, uint256 operatorNet);
    event JobRefunded(bytes32 indexed jobId, uint256 customerAmount);
    event AdvanceRepaid(bytes32 indexed jobId, uint256 principal, uint256 fee, uint256 toReserve);
    event AdvanceWrittenOff(bytes32 indexed jobId, uint256 bondSlashed, uint256 reserveUsed, uint256 socialized);
    event BondSlashed(bytes32 indexed jobId, uint256 amount);
    event ReserveDrawn(bytes32 indexed jobId, uint256 amount);
    event LossSocialized(bytes32 indexed jobId, uint256 amount);

    function setUp() public {
        usdc = new MockUSDC();
        deadline = uint64(block.timestamp + 7 days);

        vm.startPrank(ADMIN);
        vault = new JobVault(IERC20(address(usdc)));
        pool = new FloatPoolHarness(IERC20(address(usdc)));
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));
        vm.stopPrank();

        usdc.mint(LP, POOL_SEED);
        vm.startPrank(LP);
        usdc.approve(address(pool), type(uint256).max);
        pool.deposit(POOL_SEED, LP);
        vm.stopPrank();

        usdc.mint(CUSTOMER, PAYMENT);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
    }

    // ── helpers ──────────────────────────────────────────────────────────

    function _fundedJob() internal {
        vm.prank(ADMIN);
        vault.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);
        vm.prank(CUSTOMER);
        vault.fund(JOB);
    }

    function _delivered() internal {
        _fundedJob();
        vm.startPrank(OPERATOR);
        vault.startWork(JOB);
        vault.submitDelivery(JOB, DELIV);
        vm.stopPrank();
    }

    function _deliveredWithAdvance() internal {
        _fundedJob();
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);
        vm.startPrank(OPERATOR);
        vault.startWork(JOB);
        vault.submitDelivery(JOB, DELIV);
        vm.stopPrank();
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-12 — the settlement waterfall
    // ─────────────────────────────────────────────────────────────────────

    /// The demo's closing arithmetic (PRD §15.2): pool receives 12.75, operator receives 12.25.
    function test_acceptDelivery_demoNumbers() public {
        _deliveredWithAdvance();

        uint256 poolBefore = usdc.balanceOf(address(pool));
        assertEq(usdc.balanceOf(OPERATOR), PRINCIPAL, "operator holds the 12.50 advance");

        vm.expectEmit(true, true, true, true);
        emit JobSettled(JOB, OWED, OPNET);

        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);

        assertEq(usdc.balanceOf(address(pool)) - poolBefore, OWED, "pool received 12.75");
        assertEq(usdc.balanceOf(OPERATOR), PRINCIPAL + OPNET, "operator holds 12.50 advance + 12.25 net");
        assertEq(usdc.balanceOf(address(vault)), 0, "escrow fully distributed");
        assertEq(uint8(vault.jobStatus(JOB)), uint8(JobVault.JobStatus.Accepted));
    }

    /// SC-JV-009 / ADR-010: seniority is enforced by ordering INSIDE the transaction, not by
    /// convention. MockUSDC logs every movement, so we can assert the pool was paid before
    /// the operator rather than merely in the same tx.
    function test_acceptDelivery_poolIsPaidBeforeOperator() public {
        _deliveredWithAdvance();

        uint256 mark = usdc.transferCount();

        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);

        // Find the first movement into each destination after the mark.
        uint256 poolIdx = type(uint256).max;
        uint256 opIdx = type(uint256).max;
        for (uint256 i = mark; i < usdc.transferCount(); i++) {
            (, address to, ) = usdc.transferLog(i);
            if (to == address(pool) && poolIdx == type(uint256).max) poolIdx = i;
            if (to == OPERATOR && opIdx == type(uint256).max) opIdx = i;
        }

        assertLt(poolIdx, type(uint256).max, "pool was paid");
        assertLt(opIdx, type(uint256).max, "operator was paid");
        assertLt(poolIdx, opIdx, "SC-JV-009: pool must be repaid BEFORE the operator");
    }

    /// A job with no advance settles the whole escrow to the operator.
    function test_acceptDelivery_withoutAdvancePaysOperatorEverything() public {
        _delivered();

        vm.expectEmit(true, true, true, true);
        emit JobSettled(JOB, 0, PAYMENT);

        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);

        assertEq(usdc.balanceOf(OPERATOR), PAYMENT, "operator receives the full 25.00");
        assertEq(usdc.balanceOf(address(vault)), 0, "escrow drained");
    }

    function test_acceptDelivery_revertsForNonCustomer() public {
        _deliveredWithAdvance();

        address[3] memory impostors = [OPERATOR, ADMIN, STRANGER];
        for (uint256 i = 0; i < impostors.length; i++) {
            vm.expectRevert(JobVault.NotAuthorized.selector);
            vm.prank(impostors[i]);
            vault.acceptDelivery(JOB);
        }
    }

    /// SC-JV-004: only a Delivered job can be accepted, so a delivery hash always exists.
    function test_acceptDelivery_revertsBeforeDelivery() public {
        _fundedJob();
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);
    }

    /// The waterfall runs exactly once. A second acceptance must not double-pay anyone.
    function test_acceptDelivery_revertsOnDoubleAcceptance() public {
        _deliveredWithAdvance();

        vm.startPrank(CUSTOMER);
        vault.acceptDelivery(JOB);

        vm.expectRevert(JobVault.InvalidStatus.selector);
        vault.acceptDelivery(JOB);
        vm.stopPrank();

        assertEq(usdc.balanceOf(OPERATOR), PRINCIPAL + OPNET, "operator paid once");
    }

    function test_acceptDelivery_revertsWhenUnwired() public {
        MockUSDC token = new MockUSDC();
        vm.prank(ADMIN);
        JobVault orphan = new JobVault(IERC20(address(token)));

        token.mint(CUSTOMER, PAYMENT);
        vm.startPrank(CUSTOMER);
        token.approve(address(orphan), type(uint256).max);
        vm.stopPrank();

        vm.prank(ADMIN);
        orphan.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);
        vm.prank(CUSTOMER);
        orphan.fund(JOB);
        vm.startPrank(OPERATOR);
        orphan.startWork(JOB);
        orphan.submitDelivery(JOB, DELIV);
        vm.stopPrank();

        vm.expectRevert(JobVault.NotWired.selector);
        vm.prank(CUSTOMER);
        orphan.acceptDelivery(JOB);
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-13 — the flywheel, and SC-FP-005 fee split
    // ─────────────────────────────────────────────────────────────────────

    /// The 2:35 beat: repayment increments acceptedJobs, so the rate ticks 50% -> 55%.
    function test_acceptDelivery_raisesAdvanceRate() public {
        _deliveredWithAdvance();
        assertEq(pool.advanceRate(OPERATOR), 5000, "before acceptance: 50%");

        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);

        assertEq(pool.acceptedJobs(OPERATOR), 1, "one delivered job on record");
        assertEq(pool.advanceRate(OPERATOR), 5500, "after acceptance: 55% - cheaper capital, earned");
    }

    /// SC-FP-005: 20% of the fee goes to the first-loss reserve, the rest is LP yield.
    function test_acceptDelivery_splitsFeeToReserve() public {
        _deliveredWithAdvance();

        vm.expectEmit(true, true, true, true);
        emit AdvanceRepaid(JOB, PRINCIPAL, FEE, 50_000); // 20% of 0.25 == 0.05

        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);

        assertEq(pool.reserve(), 50_000, "reserve took 0.05");
        assertEq(pool.totalAssets(), POOL_SEED + (FEE - 50_000), "LPs earned 0.20");
        assertEq(pool.totalOutstanding(), 0, "advance closed");
        assertEq(pool.orgOutstanding(OPERATOR), 0, "org exposure released");
    }

    /// The advance is closed, so it cannot be repaid or written off twice.
    function test_repayAdvance_revertsWhenNoOpenAdvance() public {
        _deliveredWithAdvance();
        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);

        vm.expectRevert(FloatPool.NoOpenAdvance.selector);
        vm.prank(address(vault));
        pool.repayAdvance(JOB, OWED);
    }

    /// Partial repayment is rejected — the pool is made whole or the transaction reverts.
    function test_repayAdvance_revertsOnWrongAmount() public {
        _deliveredWithAdvance();

        vm.startPrank(address(vault));
        vm.expectRevert(FloatPool.WrongRepayment.selector);
        pool.repayAdvance(JOB, OWED - 1);

        vm.expectRevert(FloatPool.WrongRepayment.selector);
        pool.repayAdvance(JOB, OWED + 1);
        vm.stopPrank();
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-14 — refund and the SC-FP-008 loss waterfall
    // ─────────────────────────────────────────────────────────────────────

    /// SC-JV-010: the customer is made whole first, then the pool absorbs the loss.
    function test_refund_restoresCustomerThenWritesOff() public {
        _fundedJob();
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);

        assertEq(usdc.balanceOf(CUSTOMER), 0, "customer's money is escrowed");

        vm.expectEmit(true, true, true, true);
        emit JobRefunded(JOB, PAYMENT);

        vm.prank(OPERATOR);
        vault.refund(JOB);

        assertEq(usdc.balanceOf(CUSTOMER), PAYMENT, "customer made whole, in full");
        assertEq(uint8(vault.jobStatus(JOB)), uint8(JobVault.JobStatus.Refunded));

        (, , bool open) = pool.openAdvanceOf(JOB);
        assertFalse(open, "advance written off");
        assertEq(pool.writtenOffJobs(OPERATOR), 1, "write-off on the org's record");
    }

    /// SC-FP-008 stage order: bond -> reserve -> LP shares, an event per stage.
    /// With no reserve and no bond yet, the whole loss socializes to LPs.
    function test_writeOff_lossWaterfallStageOrder() public {
        _fundedJob();
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);

        vm.expectEmit(true, true, true, true);
        emit BondSlashed(JOB, 0); // SC-FP-011 bond is P1; stage emits 0 rather than being skipped
        vm.expectEmit(true, true, true, true);
        emit ReserveDrawn(JOB, 0);
        vm.expectEmit(true, true, true, true);
        emit LossSocialized(JOB, PRINCIPAL);
        vm.expectEmit(true, true, true, true);
        emit AdvanceWrittenOff(JOB, 0, 0, PRINCIPAL);

        vm.prank(OPERATOR);
        vault.refund(JOB);

        assertEq(pool.totalAssets(), POOL_SEED - PRINCIPAL, "LPs absorbed the full 12.50");
        assertEq(pool.totalOutstanding(), 0, "exposure closed");
    }

    /// With a funded reserve, the reserve absorbs first and only the excess reaches LPs.
    function test_writeOff_reserveAbsorbsBeforeLPs() public {
        // Settle one job to accrue a reserve, then default a second.
        _deliveredWithAdvance();
        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);
        assertEq(pool.reserve(), 50_000, "reserve seeded from the first job's fee");

        uint256 assetsBefore = pool.totalAssets();

        bytes32 job2 = keccak256("job_205");
        usdc.mint(CUSTOMER, PAYMENT);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
        vm.prank(ADMIN);
        vault.createJob(job2, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);
        vm.prank(CUSTOMER);
        vault.fund(job2);

        vm.prank(OPERATOR);
        uint256 principal2 = pool.requestAdvance(job2); // 55% of 25.00 == 13.75

        vm.expectEmit(true, true, true, true);
        emit ReserveDrawn(job2, 50_000);
        vm.expectEmit(true, true, true, true);
        emit LossSocialized(job2, principal2 - 50_000);

        vm.prank(OPERATOR);
        vault.refund(job2);

        assertEq(pool.reserve(), 0, "reserve fully drawn down first");
        assertEq(pool.totalAssets(), assetsBefore - (principal2 - 50_000), "LPs absorbed only the excess");
    }

    /// AT-13's other half: a write-off lowers the rate, 15 points per SC-FP-009.
    function test_writeOff_lowersAdvanceRate() public {
        _fundedJob();
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);
        assertEq(pool.advanceRate(OPERATOR), 5000, "before: 50%");

        vm.prank(OPERATOR);
        vault.refund(JOB);

        assertEq(pool.advanceRate(OPERATOR), 3500, "after a write-off: 35%");
    }

    /// Share COUNT is untouched by a loss — each share is simply worth less. That is how an
    /// ERC-4626-style vault socializes, and it keeps LP positions proportional.
    function test_writeOff_doesNotBurnShares() public {
        _fundedJob();
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);

        uint256 sharesBefore = pool.sharesOf(LP);

        vm.prank(OPERATOR);
        vault.refund(JOB);

        assertEq(pool.sharesOf(LP), sharesBefore, "share count unchanged");
        assertEq(pool.totalShares(), sharesBefore, "total shares unchanged");
        assertLt(pool.totalAssets(), POOL_SEED, "but each share is worth less");
    }

    // ─────────────────────────────────────────────────────────────────────
    // refund / cancel authorization and state
    // ─────────────────────────────────────────────────────────────────────

    /// FR-JOB-007: the customer's own exit only opens once the deadline has passed.
    function test_refund_customerMustWaitForDeadline() public {
        _fundedJob();

        vm.expectRevert(JobVault.NotAuthorized.selector);
        vm.prank(CUSTOMER);
        vault.refund(JOB);

        vm.warp(deadline);
        vm.prank(CUSTOMER);
        vault.refund(JOB);

        assertEq(usdc.balanceOf(CUSTOMER), PAYMENT, "customer recovered the escrow after the deadline");
    }

    function test_refund_revertsForStranger() public {
        _fundedJob();
        vm.warp(deadline);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vm.prank(STRANGER);
        vault.refund(JOB);
    }

    /// A settled job is terminal — no refund can claw back a completed waterfall.
    function test_refund_revertsAfterAcceptance() public {
        _deliveredWithAdvance();
        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);

        vm.expectRevert(JobVault.InvalidStatus.selector);
        vm.prank(OPERATOR);
        vault.refund(JOB);
    }

    function test_cancel_worksOnlyBeforeFunding() public {
        vm.prank(ADMIN);
        vault.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);

        vm.prank(CUSTOMER);
        vault.cancel(JOB);
        assertEq(uint8(vault.jobStatus(JOB)), uint8(JobVault.JobStatus.Cancelled));
    }

    function test_cancel_revertsOnFundedJob() public {
        _fundedJob();
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vm.prank(OPERATOR);
        vault.cancel(JOB);
    }

    // ─────────────────────────────────────────────────────────────────────
    // Conservation — nothing is created or destroyed
    // ─────────────────────────────────────────────────────────────────────

    /// Every cent of the 25.00 escrow ends up somewhere, and the pool nets exactly its fee.
    function test_settlement_conservesValue() public {
        _deliveredWithAdvance();

        uint256 totalBefore = usdc.balanceOf(address(vault)) + usdc.balanceOf(address(pool))
            + usdc.balanceOf(OPERATOR) + usdc.balanceOf(CUSTOMER);

        vm.prank(CUSTOMER);
        vault.acceptDelivery(JOB);

        uint256 totalAfter = usdc.balanceOf(address(vault)) + usdc.balanceOf(address(pool))
            + usdc.balanceOf(OPERATOR) + usdc.balanceOf(CUSTOMER);

        assertEq(totalAfter, totalBefore, "settlement moves value, never creates it");
        assertEq(usdc.balanceOf(address(pool)), POOL_SEED + FEE, "pool net position is exactly the fee");
        assertEq(usdc.balanceOf(OPERATOR), PAYMENT - FEE, "operator nets payment minus the financing fee");
    }

    /// A refund conserves value too, with the pool bearing the loss.
    function test_refund_conservesValue() public {
        _fundedJob();
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);

        uint256 totalBefore = usdc.balanceOf(address(vault)) + usdc.balanceOf(address(pool))
            + usdc.balanceOf(OPERATOR) + usdc.balanceOf(CUSTOMER);

        vm.prank(OPERATOR);
        vault.refund(JOB);

        uint256 totalAfter = usdc.balanceOf(address(vault)) + usdc.balanceOf(address(pool))
            + usdc.balanceOf(OPERATOR) + usdc.balanceOf(CUSTOMER);

        assertEq(totalAfter, totalBefore, "no value created or destroyed");
        assertEq(usdc.balanceOf(CUSTOMER), PAYMENT, "customer whole");
        assertEq(usdc.balanceOf(OPERATOR), PRINCIPAL, "operator still holds the advance - that IS the loss");
        assertEq(usdc.balanceOf(address(pool)), POOL_SEED - PRINCIPAL, "pool is down the principal");
    }

    /// Solvency, fuzzed through the full settlement path: the escrow always covers the pool,
    /// so the operator's share can never underflow regardless of rate or job size.
    function testFuzz_settlement_escrowAlwaysCoversPool(uint256 payment, uint32 accepted) public {
        payment = bound(payment, 1_000_000, 17_000_000);
        accepted = uint32(bound(accepted, 0, 7));

        pool.setHistory(OPERATOR, accepted, 0);

        bytes32 job = keccak256(abi.encode("fuzz", payment, accepted));
        usdc.mint(CUSTOMER, payment);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
        vm.prank(ADMIN);
        vault.createJob(job, CUSTOMER, OPERATOR, payment, BUDGET, TERMS, deadline);
        vm.prank(CUSTOMER);
        vault.fund(job);

        vm.prank(OPERATOR);
        pool.requestAdvance(job);
        vm.startPrank(OPERATOR);
        vault.startWork(job);
        vault.submitDelivery(job, DELIV);
        vm.stopPrank();

        (uint256 principal, uint256 fee, ) = pool.openAdvanceOf(job);
        uint256 opBefore = usdc.balanceOf(OPERATOR);

        vm.prank(CUSTOMER);
        vault.acceptDelivery(job); // must not underflow

        assertEq(usdc.balanceOf(OPERATOR) - opBefore, payment - (principal + fee), "operator nets the remainder");
        assertEq(uint8(vault.jobStatus(job)), uint8(JobVault.JobStatus.Accepted));
    }
}
