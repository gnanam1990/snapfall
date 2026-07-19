// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {IERC20, IJobVaultView} from "./interfaces.sol";
// TODO(A): after forge install, add OZ ERC4626 (or hand-rolled shares), SafeERC20, ReentrancyGuard, AccessControl (SC-FP-012)

/// @title FloatPool — receivables-secured advances against escrowed jobs (PRD §7.2, SC-FP-001..012)
/// @notice ERC-4626-style USDC vault. Advance rate is a PURE on-chain function of delivery history:
///         rate = clamp(base + growth*accepted − penalty*writeOffs, floor, cap). No oracle. (SC-FP-009)
contract FloatPool {
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
    uint256 public totalAssets;        // deposited − net outflows (define precisely in tests)
    uint256 public totalOutstanding;   // sum of open principals
    uint256 public reserve;            // first-loss buffer

    // ── Events (ABI FREEZE Fri Jul 24) ──
    event Deposited(address indexed lp, uint256 assets, uint256 shares);
    event Withdrawn(address indexed lp, uint256 assets, uint256 shares);
    event AdvanceIssued(bytes32 indexed jobId, address indexed org, uint256 principal, uint256 fee, uint16 rateBps);
    event AdvanceRepaid(bytes32 indexed jobId, uint256 principal, uint256 fee, uint256 toReserve);
    event AdvanceWrittenOff(bytes32 indexed jobId, uint256 bondSlashed, uint256 reserveUsed, uint256 socialized); // SC-FP-008 stages
    event RateChanged(address indexed org, uint16 newRateBps);

    error NotJobVault();
    error JobNotFunded();
    error DuplicateAdvance();
    error CapExceeded();
    error NotTreasury();

    constructor(IERC20 _usdc) { usdc = _usdc; admin = msg.sender; }

    /// SC-FP-009 — trustless underwriting. Pure function of contract-visible history.
    function advanceRate(address org) public view returns (uint16 bps) {
        int256 r = int256(uint256(BASE_BPS))
            + int256(uint256(GROWTH_BPS)) * int256(uint256(acceptedJobs[org]))
            - int256(uint256(PENALTY_BPS)) * int256(uint256(writtenOffJobs[org]));
        if (r < int256(uint256(FLOOR_BPS))) return FLOOR_BPS;
        if (r > int256(uint256(CAP_BPS)))   return CAP_BPS;
        return uint16(uint256(r));
    }

    // ── ERC-4626-ish LP side (P0 contract, UI is P1) ──
    function deposit(uint256 assets, address receiver) external returns (uint256 shares) { /* TODO(A) */ }
    function withdraw(uint256 assets, address receiver, address owner) external returns (uint256 shares) { /* TODO(A) */ }

    /// SC-FP-001..004: org treasury only; verify vault says Funded (read, never trust caller);
    /// one advance per job; amount = min(maxOperatingBudget, rate × customerPayment);
    /// transfer ONLY to registered org treasury; checks-effects-interactions.
    function requestAdvance(bytes32 jobId) external returns (uint256 amount) { /* TODO(A) */ }

    /// SC-FP-010: JobVault only. Split fee → reserve cut (SC-FP-005).
    function repayAdvance(bytes32 jobId, uint256 amount) external { /* TODO(A) */ }

    /// SC-FP-008 loss waterfall: bond → reserve → socialized to LP shares, events per stage.
    function writeOff(bytes32 jobId) external { /* TODO(A) */ }

    function openAdvanceOf(bytes32 jobId) external view returns (uint256 principal, uint256 fee, bool open) {
        Advance storage a = advances[jobId];
        return (a.principal, a.fee, a.status == AdvanceStatus.Issued);
    }
}
