// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

// Test law (PRD §7.4) — JobVault scope as of Jul 19.
//  [x] happy path: createJob / fund / startWork / recordExpense / submitDelivery
//  [x] unauthorized callers on every mutating function
//  [x] over-budget expense (SC-JV-003)
//  [x] illegal state transitions (SC-JV-002 amount immutable once work starts)
//  [ ] duplicate acceptance, waterfall ordering, write-off ordering, expired refund
//      -> blocked on acceptDelivery/refund/cancel, still TODO(A)

contract JobVaultTest is Test {
    JobVault internal vault;
    MockUSDC internal usdc;

    address internal constant ADMIN    = address(0xA0);
    address internal constant CUSTOMER = address(0xC0);   // "Acme Labs" (PRD §15.2)
    address internal constant OPERATOR = address(0x09);   // org treasury signer
    address internal constant STRANGER = address(0xBAD);

    bytes32 internal constant JOB   = keccak256("job_104");
    bytes32 internal constant TERMS = keccak256("competitor analysis, three AI coding products");

    uint256 internal constant PAYMENT = 25_000_000; // 25.00 USDC (PRD §15.2)
    uint256 internal constant BUDGET  =  6_000_000; //  6.00 USDC max operating budget
    uint64  internal deadline;

    // Mirrors of the frozen event signatures — expectEmit compares against these.
    event JobCreated(bytes32 indexed jobId, address indexed customer, address indexed operator, uint256 customerPayment, uint256 maxOperatingBudget, bytes32 termsHash, uint64 deadline);
    event JobFunded(bytes32 indexed jobId, uint256 amount);
    event WorkStarted(bytes32 indexed jobId);
    event ExpenseRecorded(bytes32 indexed jobId, uint256 amount, bytes32 receiptHash);
    event DeliverySubmitted(bytes32 indexed jobId, bytes32 deliveryHash);

    function setUp() public {
        usdc = new MockUSDC();
        vm.prank(ADMIN);
        vault = new JobVault(IERC20(address(usdc)));

        deadline = uint64(block.timestamp + 7 days);

        usdc.mint(CUSTOMER, PAYMENT);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
    }

    // ── helpers ──────────────────────────────────────────────────────────

    function _create() internal {
        vm.prank(ADMIN);
        vault.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);
    }

    function _fund() internal {
        _create();
        vm.prank(CUSTOMER);
        vault.fund(JOB);
    }

    function _start() internal {
        _fund();
        vm.prank(OPERATOR);
        vault.startWork(JOB);
    }

    function _status(bytes32 jobId) internal view returns (JobVault.JobStatus) {
        return vault.jobStatus(jobId);
    }

    function _expenses(bytes32 jobId) internal view returns (uint256 onchainExpenses) {
        (, , , , onchainExpenses, , , , ) = vault.jobs(jobId);
    }

    function _deliveryHash(bytes32 jobId) internal view returns (bytes32 deliveryHash) {
        (, , , , , , deliveryHash, , ) = vault.jobs(jobId);
    }

    // ─────────────────────────────────────────────────────────────────────
    // createJob
    // ─────────────────────────────────────────────────────────────────────

    function test_createJob_storesEconomicsAndEmits() public {
        vm.expectEmit(true, true, true, true);
        emit JobCreated(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);
        _create();

        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Created), "starts Created");

        (address op, uint256 payment, uint256 budget) = vault.jobEconomics(JOB);
        assertEq(op, OPERATOR, "operator recorded for SC-FP-001 lookup");
        assertEq(payment, PAYMENT, "customer payment recorded");
        assertEq(budget, BUDGET, "operating budget recorded");
    }

    function test_createJob_operatorMayCreateOwnJob() public {
        vm.prank(OPERATOR);
        vault.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);
        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Created));
    }

    function test_createJob_revertsForStranger() public {
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vm.prank(STRANGER);
        vault.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);
    }

    /// A re-created job would silently reset status and expenses — reject it.
    function test_createJob_revertsOnDuplicateId() public {
        _create();
        vm.expectRevert(JobVault.JobExists.selector);
        vm.prank(ADMIN);
        vault.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, deadline);
    }

    /// Arc forbids value transfers to the zero address outright
    /// (docs.arc.io evm-differences) — reject the address before it can reach a transfer.
    function test_createJob_revertsOnZeroAddresses() public {
        vm.startPrank(ADMIN);

        vm.expectRevert(JobVault.ZeroAddress.selector);
        vault.createJob(JOB, address(0), OPERATOR, PAYMENT, BUDGET, TERMS, deadline);

        vm.expectRevert(JobVault.ZeroAddress.selector);
        vault.createJob(JOB, CUSTOMER, address(0), PAYMENT, BUDGET, TERMS, deadline);

        vm.stopPrank();
    }

    function test_createJob_revertsOnZeroPayment() public {
        vm.expectRevert(JobVault.ZeroAmount.selector);
        vm.prank(ADMIN);
        vault.createJob(JOB, CUSTOMER, OPERATOR, 0, BUDGET, TERMS, deadline);
    }

    // ─────────────────────────────────────────────────────────────────────
    // fund — SC-JV-001
    // ─────────────────────────────────────────────────────────────────────

    function test_fund_escrowsFullPaymentAndEmits() public {
        _create();

        vm.expectEmit(true, true, true, true);
        emit JobFunded(JOB, PAYMENT);
        vm.prank(CUSTOMER);
        vault.fund(JOB);

        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Funded), "Created -> Funded");
        assertEq(usdc.balanceOf(address(vault)), PAYMENT, "escrow holds the full 25.00");
        assertEq(usdc.balanceOf(CUSTOMER), 0, "customer debited");
    }

    /// SC-JV-001 is the whole point of this function: nobody but the designated
    /// customer funds the job — not the operator, not the admin, not a stranger.
    function test_fund_revertsForAnyNonCustomer() public {
        _create();

        address[3] memory impostors = [OPERATOR, ADMIN, STRANGER];
        for (uint256 i = 0; i < impostors.length; i++) {
            usdc.mint(impostors[i], PAYMENT);
            vm.prank(impostors[i]);
            usdc.approve(address(vault), type(uint256).max);

            vm.expectRevert(JobVault.NotAuthorized.selector);
            vm.prank(impostors[i]);
            vault.fund(JOB);
        }

        assertEq(usdc.balanceOf(address(vault)), 0, "no escrow from an unauthorized funder");
    }

    function test_fund_revertsOnDoubleFunding() public {
        _fund();
        usdc.mint(CUSTOMER, PAYMENT);

        vm.expectRevert(JobVault.AlreadyFunded.selector);
        vm.prank(CUSTOMER);
        vault.fund(JOB);

        assertEq(usdc.balanceOf(address(vault)), PAYMENT, "escrow not doubled");
    }

    function test_fund_revertsOnUnknownJob() public {
        vm.expectRevert(JobVault.UnknownJob.selector);
        vm.prank(CUSTOMER);
        vault.fund(keccak256("never_created"));
    }

    /// SC-JV-002 — once work starts the escrowed amount is immutable. There is no
    /// path that mutates customerPayment, and re-funding is refused outright.
    function test_fund_revertsAfterWorkStarted() public {
        _start();
        usdc.mint(CUSTOMER, PAYMENT);

        vm.expectRevert(JobVault.InvalidStatus.selector);
        vm.prank(CUSTOMER);
        vault.fund(JOB);

        assertEq(usdc.balanceOf(address(vault)), PAYMENT, "escrow unchanged after work starts");
    }

    // ─────────────────────────────────────────────────────────────────────
    // startWork
    // ─────────────────────────────────────────────────────────────────────

    function test_startWork_transitionsFundedToInProgress() public {
        _fund();

        vm.expectEmit(true, true, true, true);
        emit WorkStarted(JOB);
        vm.prank(OPERATOR);
        vault.startWork(JOB);

        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.InProgress));
    }

    function test_startWork_revertsForNonOperator() public {
        _fund();
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vm.prank(CUSTOMER);
        vault.startWork(JOB);
    }

    /// FR-JOB-002 on-chain: no work — and therefore no paid execution — before funding.
    /// This is also the gate FloatPool reads for SC-FP-001.
    function test_startWork_revertsBeforeFunding() public {
        _create();
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vm.prank(OPERATOR);
        vault.startWork(JOB);
    }

    function test_startWork_revertsIfAlreadyStarted() public {
        _start();
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vm.prank(OPERATOR);
        vault.startWork(JOB);
    }

    // ─────────────────────────────────────────────────────────────────────
    // recordExpense — SC-JV-003
    // ─────────────────────────────────────────────────────────────────────

    function test_recordExpense_accumulatesAndEmits() public {
        _start();

        vm.expectEmit(true, true, true, true);
        emit ExpenseRecorded(JOB, 40_000, keccak256("company-profile"));
        vm.prank(OPERATOR);
        vault.recordExpense(JOB, 40_000, keccak256("company-profile")); // 0.04 USDC

        vm.prank(OPERATOR);
        vault.recordExpense(JOB, 60_000, keccak256("benchmark-summary")); // 0.06 USDC

        uint256 onchainExpenses = _expenses(JOB);
        assertEq(onchainExpenses, 100_000, "PRD 15.2 total external spend 0.10 USDC");
    }

    /// Accounting only — recordExpense must not move a cent of escrow, or the
    /// waterfall's "operator receives 12.25" arithmetic breaks. See SPEC-02.
    function test_recordExpense_movesNoEscrow() public {
        _start();
        uint256 before = usdc.balanceOf(address(vault));

        vm.prank(OPERATOR);
        vault.recordExpense(JOB, 40_000, keccak256("company-profile"));

        assertEq(usdc.balanceOf(address(vault)), before, "escrow untouched by expense recording");
    }

    function test_recordExpense_revertsForNonOperator() public {
        _start();
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vm.prank(CUSTOMER);
        vault.recordExpense(JOB, 40_000, keccak256("r"));
    }

    /// SC-JV-003 bound. The escalated 4.00 USDC purchase in the demo is well under
    /// the 6.00 budget; this covers the case where an operator tries to exceed it.
    function test_recordExpense_revertsOverBudget() public {
        _start();
        vm.expectRevert(JobVault.OverBudget.selector);
        vm.prank(OPERATOR);
        vault.recordExpense(JOB, BUDGET + 1, keccak256("too-big"));
    }

    function test_recordExpense_allowsExactlyBudget() public {
        _start();
        vm.prank(OPERATOR);
        vault.recordExpense(JOB, BUDGET, keccak256("exact"));

        uint256 onchainExpenses = _expenses(JOB);
        assertEq(onchainExpenses, BUDGET, "the bound is inclusive");
    }

    /// The bound is cumulative, not per-call — two individually-legal expenses
    /// that together exceed the budget must fail.
    function test_recordExpense_revertsWhenCumulativelyOverBudget() public {
        _start();
        vm.startPrank(OPERATOR);
        vault.recordExpense(JOB, 4_000_000, keccak256("a")); // 4.00, ok

        vm.expectRevert(JobVault.OverBudget.selector);
        vault.recordExpense(JOB, 2_000_001, keccak256("b")); // +2.000001 -> 6.000001 > 6.00
        vm.stopPrank();

        uint256 onchainExpenses = _expenses(JOB);
        assertEq(onchainExpenses, 4_000_000, "rejected expense left no residue");
    }

    function test_recordExpense_revertsOnZeroAmount() public {
        _start();
        vm.expectRevert(JobVault.ZeroAmount.selector);
        vm.prank(OPERATOR);
        vault.recordExpense(JOB, 0, keccak256("r"));
    }

    function test_recordExpense_revertsBeforeWorkStarted() public {
        _fund();
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vm.prank(OPERATOR);
        vault.recordExpense(JOB, 40_000, keccak256("r"));
    }

    function testFuzz_recordExpense_neverExceedsBudget(uint256 a, uint256 b) public {
        a = bound(a, 1, BUDGET);
        b = bound(b, 1, BUDGET);
        _start();

        vm.startPrank(OPERATOR);
        vault.recordExpense(JOB, a, keccak256("a"));
        if (a + b > BUDGET) vm.expectRevert(JobVault.OverBudget.selector);
        vault.recordExpense(JOB, b, keccak256("b"));
        vm.stopPrank();

        uint256 onchainExpenses = _expenses(JOB);
        assertLe(onchainExpenses, BUDGET, "SC-JV-003 bound can never be breached");
    }

    // ─────────────────────────────────────────────────────────────────────
    // submitDelivery — SC-JV-004
    // ─────────────────────────────────────────────────────────────────────

    function test_submitDelivery_setsHashAndTransitions() public {
        _start();
        bytes32 h = keccak256("competitor-analysis.pdf");

        vm.expectEmit(true, true, true, true);
        emit DeliverySubmitted(JOB, h);
        vm.prank(OPERATOR);
        vault.submitDelivery(JOB, h);

        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Delivered), "InProgress -> Delivered");

        bytes32 deliveryHash = _deliveryHash(JOB);
        assertEq(deliveryHash, h, "content hash stored (hash only, never content)");
    }

    function test_submitDelivery_revertsForNonOperator() public {
        _start();
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vm.prank(CUSTOMER);
        vault.submitDelivery(JOB, keccak256("forged"));
    }

    /// SC-JV-004 — an empty hash would let a job reach Delivered with nothing to verify.
    function test_submitDelivery_revertsOnZeroHash() public {
        _start();
        vm.expectRevert(JobVault.ZeroHash.selector);
        vm.prank(OPERATOR);
        vault.submitDelivery(JOB, bytes32(0));
    }

    function test_submitDelivery_revertsBeforeWorkStarted() public {
        _fund();
        vm.expectRevert(JobVault.InvalidStatus.selector);
        vm.prank(OPERATOR);
        vault.submitDelivery(JOB, keccak256("early"));
    }

    /// Re-submitting would swap the deliverable out from under a customer who is
    /// mid-review. Delivered is terminal for this function.
    function test_submitDelivery_revertsOnResubmission() public {
        _start();
        vm.startPrank(OPERATOR);
        vault.submitDelivery(JOB, keccak256("v1"));

        vm.expectRevert(JobVault.InvalidStatus.selector);
        vault.submitDelivery(JOB, keccak256("v2"));
        vm.stopPrank();
    }

    // ─────────────────────────────────────────────────────────────────────
    // Demo spine (PRD §15.2) — everything implemented so far, in order
    // ─────────────────────────────────────────────────────────────────────

    /// Walks the Jul 19 slice of the demo: 25.00 USDC job created, funded by Acme Labs,
    /// work started, 0.10 USDC of purchases recorded, report hash submitted.
    /// Escrow must still hold the full 25.00 — the waterfall has not run yet.
    function test_demoSpine_throughDelivery() public {
        _create();

        vm.prank(CUSTOMER);
        vault.fund(JOB);
        assertEq(usdc.balanceOf(address(vault)), 25_000_000, "25.00 escrowed");

        vm.startPrank(OPERATOR);
        vault.startWork(JOB);
        vault.recordExpense(JOB, 40_000, keccak256("company-profile"));    // 0.04
        vault.recordExpense(JOB, 60_000, keccak256("benchmark-summary"));  // 0.06
        vault.submitDelivery(JOB, keccak256("competitor-analysis.pdf"));
        vm.stopPrank();

        assertEq(uint8(_status(JOB)), uint8(JobVault.JobStatus.Delivered));

        uint256 onchainExpenses = _expenses(JOB);
        assertEq(onchainExpenses, 100_000, "0.10 USDC total external spend");
        assertEq(usdc.balanceOf(address(vault)), 25_000_000, "escrow intact until acceptance");
    }
}
