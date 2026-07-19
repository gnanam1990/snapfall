// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {IFloatPool} from "./interfaces.sol";

/// @title JobVault — customer escrow + settlement waterfall (PRD §7.1, SC-JV-001..010)
/// @notice Holds customer-funded USDC per job; on acceptance executes the waterfall:
///         repay FloatPool (principal + fee) FIRST, then release remainder to operator — one tx.
contract JobVault is ReentrancyGuard {
    using SafeERC20 for IERC20;

    enum JobStatus { Created, Funded, InProgress, Delivered, Accepted, Refunded, Cancelled }

    struct Job {
        address customer;
        address operator;          // organization treasury signer
        uint256 customerPayment;
        uint256 maxOperatingBudget;
        uint256 onchainExpenses;
        bytes32 termsHash;
        bytes32 deliveryHash;
        uint64  deadline;
        JobStatus status;
    }

    IERC20 public immutable usdc;
    IFloatPool public floatPool;   // set once by admin; repay/writeOff callee
    address public admin;

    mapping(bytes32 => Job) public jobs;

    // ── Events (ABI FREEZE Fri Jul 24 — additions ok, changes need all-three sign-off) ──
    event JobCreated(bytes32 indexed jobId, address indexed customer, address indexed operator, uint256 customerPayment, uint256 maxOperatingBudget, bytes32 termsHash, uint64 deadline);
    event JobFunded(bytes32 indexed jobId, uint256 amount);
    event WorkStarted(bytes32 indexed jobId);
    event ExpenseRecorded(bytes32 indexed jobId, uint256 amount, bytes32 receiptHash);
    event DeliverySubmitted(bytes32 indexed jobId, bytes32 deliveryHash);
    event JobSettled(bytes32 indexed jobId, uint256 advanceRepaid, uint256 operatorNet);   // SC-JV-009
    event JobRefunded(bytes32 indexed jobId, uint256 customerAmount);
    event JobCancelled(bytes32 indexed jobId);

    error InvalidStatus();
    error NotAuthorized();
    error OverBudget();
    error AlreadyFunded();
    // ── Added Jul 19 (pre-freeze). Error selectors are ABI surface: no further additions after Jul 24. ──
    error JobExists();
    error UnknownJob();
    error ZeroAddress();
    error ZeroAmount();
    error ZeroHash();

    constructor(IERC20 _usdc) { usdc = _usdc; admin = msg.sender; }

    // ─────────────────────────────────────────────────────────────────────
    // Creation
    // ─────────────────────────────────────────────────────────────────────

    /// @notice Register a job before funding. Callable by the admin (demo seeding) or by the
    ///         designated operator (self-service). The customer is designated here and is the
    ///         ONLY address that may later fund it (SC-JV-001).
    /// @dev Not in the Jul 19 task list, but fund/startWork/recordExpense/submitDelivery are all
    ///      unreachable without it. Kept deliberately thin — no unspecified constraints.
    function createJob(
        bytes32 jobId,
        address customer,
        address operator,
        uint256 customerPayment,
        uint256 maxOperatingBudget,
        bytes32 termsHash,
        uint64 deadline
    ) external {
        if (msg.sender != admin && msg.sender != operator) revert NotAuthorized();
        if (jobs[jobId].customer != address(0)) revert JobExists();
        if (customer == address(0) || operator == address(0)) revert ZeroAddress();
        if (customerPayment == 0) revert ZeroAmount();

        jobs[jobId] = Job({
            customer: customer,
            operator: operator,
            customerPayment: customerPayment,
            maxOperatingBudget: maxOperatingBudget,
            onchainExpenses: 0,
            termsHash: termsHash,
            deliveryHash: bytes32(0),
            deadline: deadline,
            status: JobStatus.Created
        });

        emit JobCreated(jobId, customer, operator, customerPayment, maxOperatingBudget, termsHash, deadline);
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-JV-001 / SC-JV-002 — funding
    // ─────────────────────────────────────────────────────────────────────

    /// @notice Customer escrows the full quoted amount. Only the designated customer may fund.
    /// @dev SC-JV-002: the funded amount is fixed at creation and never mutated afterwards,
    ///      so it is immutable by construction once work starts. CEI: status flips before the
    ///      token pull, and nonReentrant guards the callback surface of a hostile token.
    function fund(bytes32 jobId) external nonReentrant {
        Job storage j = jobs[jobId];
        if (j.customer == address(0)) revert UnknownJob();
        if (msg.sender != j.customer) revert NotAuthorized();          // SC-JV-001
        if (j.status == JobStatus.Funded) revert AlreadyFunded();
        if (j.status != JobStatus.Created) revert InvalidStatus();

        uint256 amount = j.customerPayment;
        j.status = JobStatus.Funded;                                    // effect
        emit JobFunded(jobId, amount);                                  // SC-JV-007

        usdc.safeTransferFrom(msg.sender, address(this), amount);       // interaction
    }

    // ─────────────────────────────────────────────────────────────────────
    // Lifecycle
    // ─────────────────────────────────────────────────────────────────────

    /// @notice Operator starts work on a funded job. Funded → InProgress.
    /// @dev The Funded gate is what FR-JOB-002 ("no paid execution until funding confirmed")
    ///      reduces to on-chain, and it is also the state FloatPool reads for SC-FP-001.
    function startWork(bytes32 jobId) external {
        Job storage j = jobs[jobId];
        if (j.customer == address(0)) revert UnknownJob();
        if (msg.sender != j.operator) revert NotAuthorized();
        if (j.status != JobStatus.Funded) revert InvalidStatus();

        j.status = JobStatus.InProgress;
        emit WorkStarted(jobId);
    }

    /// @notice Record an approved on-chain expense against the job's operating budget.
    /// @dev SC-JV-003 — operator only, bounded by maxOperatingBudget. This is ACCOUNTING ONLY:
    ///      it moves no USDC. In the demo, agent purchases are paid from the advance sitting in
    ///      the treasury (x402, off-vault), never from escrow — so escrow stays whole for the
    ///      waterfall and PRD §15.2's "operator receives 12.25" arithmetic holds.
    ///      SC-JV-003's wording is "records or releases"; the releasing reading is unresolved —
    ///      see docs/OPEN-SPEC-QUESTIONS.md SPEC-02.
    function recordExpense(bytes32 jobId, uint256 amount, bytes32 receiptHash) external {
        Job storage j = jobs[jobId];
        if (j.customer == address(0)) revert UnknownJob();
        if (msg.sender != j.operator) revert NotAuthorized();           // SC-JV-003
        if (j.status != JobStatus.InProgress) revert InvalidStatus();
        if (amount == 0) revert ZeroAmount();

        uint256 spent = j.onchainExpenses + amount;
        if (spent > j.maxOperatingBudget) revert OverBudget();          // SC-JV-003 bound

        j.onchainExpenses = spent;
        emit ExpenseRecorded(jobId, amount, receiptHash);               // SC-JV-007
    }

    /// @notice Attach the deliverable's content hash. InProgress → Delivered.
    /// @dev SC-JV-004 — the hash must exist before acceptance can settle. Enforced structurally:
    ///      acceptDelivery only accepts the Delivered state, which only this function can set.
    ///      FR-AUD-003 / SC-AA-003: a hash, never content.
    function submitDelivery(bytes32 jobId, bytes32 deliveryHash) external {
        Job storage j = jobs[jobId];
        if (j.customer == address(0)) revert UnknownJob();
        if (msg.sender != j.operator) revert NotAuthorized();
        if (j.status != JobStatus.InProgress) revert InvalidStatus();
        if (deliveryHash == bytes32(0)) revert ZeroHash();              // SC-JV-004

        j.deliveryHash = deliveryHash;
        j.status = JobStatus.Delivered;
        emit DeliverySubmitted(jobId, deliveryHash);
    }

    /// SC-JV-005 + SC-JV-009: acceptance executes the waterfall atomically.
    /// Order: (1) query FloatPool.openAdvanceOf(jobId); (2) transfer principal+fee to pool via repayAdvance;
    /// (3) transfer remainder to operator; (4) emit JobSettled. Checks-effects-interactions throughout.
    function acceptDelivery(bytes32 jobId) external { /* TODO(A): customer only; Delivered→Accepted */ }

    // ── SC-JV-006 + SC-JV-010: refund/cancel constrained by state/deadline/spend; notify pool writeOff ──
    function refund(bytes32 jobId) external { /* TODO(A) */ }
    function cancel(bytes32 jobId) external { /* TODO(A) */ }

    // Views for FloatPool verification (SC-FP-001 reads vault state, never trusts caller)
    function jobStatus(bytes32 jobId) external view returns (JobStatus) { return jobs[jobId].status; }
    function jobEconomics(bytes32 jobId) external view returns (address, uint256, uint256) {
        Job storage j = jobs[jobId];
        return (j.operator, j.customerPayment, j.maxOperatingBudget);
    }
}
