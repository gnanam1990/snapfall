// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {SafeCast} from "@openzeppelin/contracts/utils/math/SafeCast.sol";
import {IJobVaultView} from "./interfaces.sol";

/// @title FloatPool — receivables-secured advances against escrowed jobs (PRD §7.2, SC-FP-001..012)
/// @notice ERC-4626-style USDC vault. Advance rate is a PURE on-chain function of delivery history:
///         rate = clamp(base + growth*accepted − penalty*writeOffs, floor, cap). No oracle. (SC-FP-009)
contract FloatPool is ReentrancyGuard {
    using SafeERC20 for IERC20;

    enum AdvanceStatus { None, Issued, Repaid, WrittenOff }

    struct Advance {
        bytes32 jobId;
        address operatorOrg;
        uint256 principal;
        uint256 fee;            // FEE_BPS of principal
        uint64  openedAt;
        AdvanceStatus status;
    }

    // ── Rate function params (defaults per SC-FP-009) ──
    uint16 public constant BASE_BPS    = 5000; // 50%
    uint16 public constant GROWTH_BPS  = 500;  // +5% per accepted job
    uint16 public constant PENALTY_BPS = 1500; // −15% per write-off
    uint16 public constant FLOOR_BPS   = 3000; // 30%
    uint16 public constant CAP_BPS     = 8500; // 85%
    uint16 public constant FEE_BPS     = 200;  // 2% of principal (SC-FP-005)
    uint16 public constant RESERVE_CUT_BPS = 2000; // 20% of fees → first-loss reserve
    uint16 public constant ORG_EXPOSURE_CAP_BPS = 1000; // ≤10% TVL per org (SC-FP-006)
    uint16 public constant UTILIZATION_CAP_BPS  = 8000; // ≤80% global

    IERC20 public immutable usdc;
    IJobVaultView public jobVault;      // set once; SC-FP-010: repay/writeOff callable only by vault
    address public admin;

    mapping(bytes32 => Advance) public advances;           // one advance per job (SC-FP-003)
    mapping(address => uint32) public acceptedJobs;        // org → count
    mapping(address => uint32) public writtenOffJobs;      // org → count
    mapping(address => uint256) public orgOutstanding;     // org → drawn principal
    uint256 public totalAssets;        // LP-owned capital: idle LP cash + totalOutstanding
    uint256 public totalOutstanding;   // sum of open principals
    uint256 public reserve;            // first-loss buffer (SC-FP-005); NOT part of totalAssets

    // ── Added Jul 19 (pre-freeze): LP share accounting ──
    mapping(address => uint256) public sharesOf;
    uint256 public totalShares;

    // ── Events (ABI FREEZE Fri Jul 24) ──
    event Deposited(address indexed lp, uint256 assets, uint256 shares);
    event Withdrawn(address indexed lp, uint256 assets, uint256 shares);
    event AdvanceIssued(bytes32 indexed jobId, address indexed org, uint256 principal, uint256 fee, uint16 rateBps);
    event AdvanceRepaid(bytes32 indexed jobId, uint256 principal, uint256 fee, uint256 toReserve);
    event AdvanceWrittenOff(bytes32 indexed jobId, uint256 bondSlashed, uint256 reserveUsed, uint256 socialized); // SC-FP-008 stages
    event RateChanged(address indexed org, uint16 newRateBps);
    // ── Added Jul 19 (pre-freeze) ──
    event Wired(address indexed jobVault);                          // SPEC-04
    event BondSlashed(bytes32 indexed jobId, uint256 amount);       // SC-FP-008 stage 1
    event ReserveDrawn(bytes32 indexed jobId, uint256 amount);      // SC-FP-008 stage 2
    event LossSocialized(bytes32 indexed jobId, uint256 amount);    // SC-FP-008 stage 3

    error NotJobVault();
    error JobNotFunded();
    error DuplicateAdvance();
    error CapExceeded();
    error NotTreasury();
    // ── Added Jul 19 (pre-freeze) ──
    error NotAuthorized();
    error ZeroAddress();
    error AlreadyWired();
    error NotWired();
    error NoOpenAdvance();
    error WrongRepayment();
    error ZeroAmount();
    error InsufficientLiquidity();

    constructor(IERC20 _usdc) { usdc = _usdc; admin = msg.sender; }

    /// SPEC-04 — set-once wiring. SC-FP-010 depends on this being set: repayAdvance and
    /// writeOff are callable only by the registered JobVault, and "registered" means here.
    function wireJobVault(address vault) external {
        if (msg.sender != admin) revert NotAuthorized();
        if (address(jobVault) != address(0)) revert AlreadyWired();
        if (vault == address(0)) revert ZeroAddress();
        jobVault = IJobVaultView(vault);
        emit Wired(vault);
    }

    /// SC-FP-010 — repay/writeOff are JobVault-only. An unwired pool rejects both,
    /// so a half-deployed system fails loudly instead of silently accepting calls.
    modifier onlyJobVault() {
        if (address(jobVault) == address(0)) revert NotWired();
        if (msg.sender != address(jobVault)) revert NotJobVault();
        _;
    }

    /// SC-FP-009 — trustless underwriting. Pure function of contract-visible history.
    ///
    /// Computed entirely in uint256: the penalty is compared against the base rather than
    /// subtracted from it, so the "more write-offs than credit" case saturates at the floor
    /// instead of going negative. The result is clamped into [FLOOR, CAP] before the cast,
    /// so the uint16 narrowing is provably lossless — CAP_BPS (8500) fits uint16 with room.
    function advanceRate(address org) public view returns (uint16 bps) {
        uint256 base = uint256(BASE_BPS) + uint256(GROWTH_BPS) * uint256(acceptedJobs[org]);
        uint256 penalty = uint256(PENALTY_BPS) * uint256(writtenOffJobs[org]);

        uint256 r = penalty >= base ? uint256(FLOOR_BPS) : base - penalty;
        if (r < uint256(FLOOR_BPS)) r = uint256(FLOOR_BPS);
        if (r > uint256(CAP_BPS)) r = uint256(CAP_BPS);

        // SafeCast reverts rather than truncating, so the narrowing is checked at runtime
        // as well as being provable from the clamp above. No lint suppression needed.
        return SafeCast.toUint16(r);
    }

    // ─────────────────────────────────────────────────────────────────────
    // ERC-4626-ish LP side (P0 contract, UI is P1)
    //
    // Accounting model, stated once so the invariants below are checkable:
    //   totalAssets      = LP-owned capital = idle LP cash + totalOutstanding. EXCLUDES reserve.
    //   totalOutstanding = principal currently lent out.
    //   reserve          = first-loss buffer, funded from the fee cut (SC-FP-005). Not LP capital.
    //   usdc.balanceOf(pool) = idle LP cash + reserve.
    // Issuing an advance moves capital from cash to receivable; totalAssets is unchanged.
    // ─────────────────────────────────────────────────────────────────────

    /// @notice LP deposits USDC and receives proportional shares.
    function deposit(uint256 assets, address receiver) external nonReentrant returns (uint256 shares) {
        if (assets == 0) revert ZeroAmount();
        if (receiver == address(0)) revert ZeroAddress();

        // First depositor sets the 1:1 exchange rate; afterwards shares track LP capital.
        shares = totalShares == 0 ? assets : (assets * totalShares) / totalAssets;
        if (shares == 0) revert ZeroAmount();

        totalShares += shares;                                      // effects
        sharesOf[receiver] += shares;
        totalAssets += assets;
        emit Deposited(receiver, assets, shares);

        usdc.safeTransferFrom(msg.sender, address(this), assets);   // interaction
    }

    /// @notice LP redeems shares for USDC. Only capital that is not lent out may leave.
    function withdraw(uint256 assets, address receiver, address owner) external nonReentrant returns (uint256 shares) {
        if (assets == 0) revert ZeroAmount();
        if (receiver == address(0)) revert ZeroAddress();
        // No allowance system in the MVP: an LP withdraws their own position only.
        if (owner != msg.sender) revert NotAuthorized();

        // Round up, so rounding dust is charged to the withdrawer rather than the pool.
        shares = (assets * totalShares + totalAssets - 1) / totalAssets;
        if (shares > sharesOf[owner]) revert InsufficientLiquidity();

        // Outstanding principal is deployed and cannot be redeemed until it is repaid.
        uint256 idle = totalAssets - totalOutstanding;
        if (assets > idle) revert InsufficientLiquidity();

        totalShares -= shares;                                      // effects
        sharesOf[owner] -= shares;
        totalAssets -= assets;
        emit Withdrawn(owner, assets, shares);

        usdc.safeTransfer(receiver, assets);                        // interaction
    }

    /// @notice Draw a receivables-secured advance against a Funded job (SC-FP-001..006).
    ///
    /// SPEC-01 RULING (19 Jul 2026): `advance = advanceRate(org) × customerPayment`.
    /// The `min(maxOperatingBudget, ...)` term is GONE. maxOperatingBudget is the SC-JV-003
    /// spend bound and has nothing to do with borrowing capacity.
    ///
    /// Solvency holds by construction, not by a cap: the most a job can ever owe back is
    /// CAP_BPS + fee on that principal = 85% × 1.02 = 86.7% of customerPayment, and the full
    /// customerPayment is already escrowed in the JobVault. The waterfall therefore cannot
    /// come up short. `testFuzz_advance_neverExceedsEscrow` asserts this over the whole
    /// rate range and random org histories.
    function requestAdvance(bytes32 jobId) external nonReentrant returns (uint256 amount) {
        if (address(jobVault) == address(0)) revert NotWired();

        // SC-FP-001: read the vault's own view of the job. The caller is never trusted for
        // status, economics, or identity — all three come from the vault.
        if (jobVault.jobStatus(jobId) != IJobVaultView.JobStatus.Funded) revert JobNotFunded();

        (address org, uint256 customerPayment, ) = jobVault.jobEconomics(jobId);

        // SC-FP-004: only the treasury registered for THIS job may draw against it.
        if (msg.sender != org) revert NotTreasury();

        // SC-FP-003: exactly one advance per job.
        if (advances[jobId].status != AdvanceStatus.None) revert DuplicateAdvance();

        uint16 rateBps = advanceRate(org);
        uint256 principal = (customerPayment * uint256(rateBps)) / 10_000;
        if (principal == 0) revert ZeroAmount();
        uint256 fee = (principal * uint256(FEE_BPS)) / 10_000;

        // Liquidity: only idle LP cash can be lent.
        if (principal > totalAssets - totalOutstanding) revert InsufficientLiquidity();

        // ── effects, before any transfer (CEI) ──
        advances[jobId] = Advance({
            jobId: jobId,
            operatorOrg: org,
            principal: principal,
            fee: fee,
            openedAt: uint64(block.timestamp),
            status: AdvanceStatus.Issued
        });
        orgOutstanding[org] += principal;
        totalOutstanding += principal;

        // SC-FP-006: caps are checked against the post-issuance position, so an advance that
        // would breach either one never lands.
        if (orgOutstanding[org] * 10_000 > uint256(ORG_EXPOSURE_CAP_BPS) * totalAssets) revert CapExceeded();
        if (totalOutstanding * 10_000 > uint256(UTILIZATION_CAP_BPS) * totalAssets) revert CapExceeded();

        emit AdvanceIssued(jobId, org, principal, fee, rateBps);

        // SC-FP-004: pay the registered treasury read from the vault — never an address
        // supplied by the caller, and never an agent wallet.
        usdc.safeTransfer(org, principal);                          // interaction

        return principal;
    }

    /// SC-FP-010: JobVault only. Split fee → reserve cut (SC-FP-005).
    function repayAdvance(bytes32 jobId, uint256 amount) external onlyJobVault { /* TODO(A) */ }

    /// SC-FP-008 loss waterfall: bond → reserve → socialized to LP shares, events per stage.
    function writeOff(bytes32 jobId) external onlyJobVault { /* TODO(A) */ }

    function openAdvanceOf(bytes32 jobId) external view returns (uint256 principal, uint256 fee, bool open) {
        Advance storage a = advances[jobId];
        return (a.principal, a.fee, a.status == AdvanceStatus.Issued);
    }
}
