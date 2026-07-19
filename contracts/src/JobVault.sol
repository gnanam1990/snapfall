// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {IERC20, IFloatPool} from "./interfaces.sol";
// TODO(A): after forge install, switch to OZ SafeERC20 + ReentrancyGuard + AccessControl (PRD SC-JV-008)

/// @title JobVault — customer escrow + settlement waterfall (PRD §7.1, SC-JV-001..010)
/// @notice Holds customer-funded USDC per job; on acceptance executes the waterfall:
///         repay FloatPool (principal + fee) FIRST, then release remainder to operator — one tx.
contract JobVault {
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

    constructor(IERC20 _usdc) { usdc = _usdc; admin = msg.sender; }

    // ── SC-JV-001: only designated customer funds (demo sponsor mode behind explicit flag) ──
    function createJob(bytes32 jobId, address customer, address operator, uint256 customerPayment, uint256 maxOperatingBudget, bytes32 termsHash, uint64 deadline) external { /* TODO(A) */ }

    function fund(bytes32 jobId) external { /* TODO(A): pull USDC from designated customer; status Created→Funded; SC-JV-002 amount immutable after start */ }

    function startWork(bytes32 jobId) external { /* TODO(A): operator only; Funded→InProgress */ }

    // ── SC-JV-003: operator-only, bounded by maxOperatingBudget ──
    function recordExpense(bytes32 jobId, uint256 amount, bytes32 receiptHash) external { /* TODO(A) */ }

    // ── SC-JV-004: delivery hash before acceptance ──
    function submitDelivery(bytes32 jobId, bytes32 deliveryHash) external { /* TODO(A): InProgress→Delivered */ }

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
