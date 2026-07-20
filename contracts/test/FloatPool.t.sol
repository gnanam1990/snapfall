// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";

// Test law (PRD §7.4) — required cases. Checked items are covered in THIS file.
//  [x] advanceRate: base 50%, +5%/accepted, −15%/writeOff, clamps at 30%/85%
//  [ ] requestAdvance: reverts unless vault says Funded; duplicate reverts; amount = min(budget, rate×payment)
//  [ ] advance transfers ONLY to registered treasury
//  [ ] waterfall ordering: pool repaid (principal+fee) BEFORE operator transfer, same tx
//  [ ] write-off waterfall ordering: bond → reserve → LP shares, events per stage
//  [ ] exposure cap (10% TVL) + utilization cap (80%) enforced
//  [ ] reentrancy assumptions; fuzz: share/amount accounting invariants

/// @dev Test-only harness. `acceptedJobs` / `writtenOffJobs` are written in production
///      solely by repayAdvance/writeOff (still TODO(A)), so the rate function cannot be
///      exercised without seeding history. Setters live HERE, never on FloatPool —
///      the ABI freezes Fri Jul 24 and test scaffolding must not leak into it.
contract FloatPoolHarness is FloatPool {
    constructor(IERC20 _usdc) FloatPool(_usdc) {}

    function setHistory(address org, uint32 accepted, uint32 writtenOff) external {
        acceptedJobs[org] = accepted;
        writtenOffJobs[org] = writtenOff;
    }
}

contract FloatPoolRateTest is Test {
    FloatPoolHarness internal pool;
    address internal constant ORG = address(0xA11CE);

    function setUp() public {
        // advanceRate() never touches the token; address(0) is sufficient and keeps
        // these tests a pure unit of SC-FP-009.
        pool = new FloatPoolHarness(IERC20(address(0)));
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-009 — base case
    // ─────────────────────────────────────────────────────────────────────

    /// A brand-new org with no history borrows at the 50% base rate.
    /// This is the demo's opening number (PRD §15.2: "Advance rate (job 1) | 50%").
    function test_advanceRate_baseIsFiftyPercent() public view {
        assertEq(pool.advanceRate(ORG), 5000, "virgin org must price at BASE_BPS");
    }

    /// Constants must match SC-FP-009 exactly — a silent constant edit would
    /// change every downstream number in the demo without failing anything else.
    function test_rateConstants_matchSpec() public view {
        assertEq(pool.BASE_BPS(), 5000, "SC-FP-009 base 50%");
        assertEq(pool.GROWTH_BPS(), 500, "SC-FP-009 +5% per accepted job");
        assertEq(pool.PENALTY_BPS(), 1500, "SC-FP-009 -15% per write-off");
        assertEq(pool.FLOOR_BPS(), 3000, "SC-FP-009 floor 30%");
        assertEq(pool.CAP_BPS(), 8500, "SC-FP-009 cap 85%");
        assertEq(pool.FEE_BPS(), 200, "SC-FP-005 fee 200 bps");
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-009 — growth on accepted jobs (the flywheel)
    // ─────────────────────────────────────────────────────────────────────

    /// AT-13 (rate flywheel), the 2:35 demo beat: one accepted job moves 50% → 55%.
    function test_advanceRate_risesFivePointsPerAcceptedJob() public {
        pool.setHistory(ORG, 1, 0);
        assertEq(pool.advanceRate(ORG), 5500, "1 accepted -> 55% (PRD 15.2 post-job rate)");

        pool.setHistory(ORG, 2, 0);
        assertEq(pool.advanceRate(ORG), 6000, "2 accepted -> 60%");

        pool.setHistory(ORG, 5, 0);
        assertEq(pool.advanceRate(ORG), 7500, "5 accepted -> 75%");
    }

    /// 7 accepted lands exactly on the cap — the boundary must be inclusive, not clamped early.
    function test_advanceRate_reachesCapExactly() public {
        pool.setHistory(ORG, 7, 0);
        assertEq(pool.advanceRate(ORG), 8500, "5000 + 7*500 == CAP_BPS exactly");
    }

    /// SC-FP-009 cap 85% — the anti-rate-gaming ceiling (§9.3 "self-dealing fake jobs").
    function test_advanceRate_clampsAtCap() public {
        pool.setHistory(ORG, 8, 0);
        assertEq(pool.advanceRate(ORG), 8500, "8 accepted would be 90% -> clamped to cap");

        pool.setHistory(ORG, 1000, 0);
        assertEq(pool.advanceRate(ORG), 8500, "no amount of history exceeds the cap");
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-009 — penalty on write-offs
    // ─────────────────────────────────────────────────────────────────────

    function test_advanceRate_fallsFifteenPointsPerWriteOff() public {
        pool.setHistory(ORG, 0, 1);
        assertEq(pool.advanceRate(ORG), 3500, "1 write-off -> 35%");
    }

    /// SC-FP-009 floor 30%.
    function test_advanceRate_clampsAtFloor() public {
        pool.setHistory(ORG, 0, 2);
        assertEq(pool.advanceRate(ORG), 3000, "2 write-offs would be 20% -> clamped to floor");
    }

    /// The penalty far exceeds the base here. advanceRate() compares penalty against base
    /// instead of subtracting, so this saturates at the floor rather than underflowing a
    /// uint256 — if someone "simplifies" it to a plain subtraction, this test is the tripwire.
    function test_advanceRate_survivesPenaltyExceedingBase() public {
        pool.setHistory(ORG, 0, 1000);
        assertEq(pool.advanceRate(ORG), 3000, "penalty >> base must clamp, not revert");

        pool.setHistory(ORG, 0, type(uint32).max);
        assertEq(pool.advanceRate(ORG), 3000, "max write-offs must clamp, not revert");
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-009 — mixed history
    // ─────────────────────────────────────────────────────────────────────

    function test_advanceRate_mixedHistory() public {
        // 5000 + 4*500 - 1*1500 = 5500
        pool.setHistory(ORG, 4, 1);
        assertEq(pool.advanceRate(ORG), 5500, "4 accepted, 1 write-off -> 55%");

        // 5000 + 3*500 - 2*1500 = 3500
        pool.setHistory(ORG, 3, 2);
        assertEq(pool.advanceRate(ORG), 3500, "3 accepted, 2 write-offs -> 35%");
    }

    /// A write-off costs exactly three accepted jobs' worth of credit (1500 / 500).
    /// This ratio is the whole underwriting thesis; pin it.
    function test_advanceRate_oneWriteOffCostsThreeAcceptedJobs() public {
        pool.setHistory(ORG, 3, 1);
        assertEq(pool.advanceRate(ORG), 5000, "3 accepted cancels exactly 1 write-off");
    }

    // ─────────────────────────────────────────────────────────────────────
    // Isolation + purity
    // ─────────────────────────────────────────────────────────────────────

    /// Credit history is per-org. One org's write-offs must never touch another's rate.
    function test_advanceRate_isPerOrg() public {
        address other = address(0xB0B);
        pool.setHistory(ORG, 6, 0);
        pool.setHistory(other, 0, 3);

        assertEq(pool.advanceRate(ORG), 8000, "org A unaffected by org B");
        assertEq(pool.advanceRate(other), 3000, "org B unaffected by org A");
    }

    /// SC-FP-009 "inputs read from contract-visible history only — trustless underwriting."
    /// No oracle, no caller influence: same history, same answer, regardless of who asks.
    function test_advanceRate_independentOfCaller() public {
        pool.setHistory(ORG, 2, 0);
        uint16 expected = 6000;

        vm.prank(address(0xDEAD));
        assertEq(pool.advanceRate(ORG), expected, "rate must not depend on msg.sender");

        vm.prank(ORG);
        assertEq(pool.advanceRate(ORG), expected, "not even the org itself can move its rate");
    }

    // ─────────────────────────────────────────────────────────────────────
    // Fuzz — SC-FP-009 invariants over the whole input space
    // ─────────────────────────────────────────────────────────────────────

    /// The rate is ALWAYS inside [floor, cap] for every reachable history.
    /// This is the invariant the pool's solvency caps are reasoned against.
    function testFuzz_advanceRate_alwaysWithinBounds(uint32 accepted, uint32 writtenOff) public {
        pool.setHistory(ORG, accepted, writtenOff);
        uint16 rate = pool.advanceRate(ORG);
        assertGe(rate, pool.FLOOR_BPS(), "rate below floor");
        assertLe(rate, pool.CAP_BPS(), "rate above cap");
    }

    /// Delivering more work can never make capital more expensive (monotonic non-decreasing).
    function testFuzz_advanceRate_monotonicInAcceptedJobs(uint16 accepted, uint16 writtenOff) public {
        pool.setHistory(ORG, accepted, writtenOff);
        uint16 before = pool.advanceRate(ORG);

        pool.setHistory(ORG, uint32(accepted) + 1, writtenOff);
        assertGe(pool.advanceRate(ORG), before, "an accepted job must never lower the rate");
    }

    /// A write-off can never make capital cheaper (monotonic non-increasing).
    function testFuzz_advanceRate_monotonicInWriteOffs(uint16 accepted, uint16 writtenOff) public {
        pool.setHistory(ORG, accepted, writtenOff);
        uint16 before = pool.advanceRate(ORG);

        pool.setHistory(ORG, accepted, uint32(writtenOff) + 1);
        assertLe(pool.advanceRate(ORG), before, "a write-off must never raise the rate");
    }

    /// Reference implementation of SC-FP-009, computed independently in the test.
    /// Guards against a refactor that preserves the spot checks but breaks the curve.
    function testFuzz_advanceRate_matchesSpecFormula(uint32 accepted, uint32 writtenOff) public {
        pool.setHistory(ORG, accepted, writtenOff);

        uint256 base = uint256(pool.BASE_BPS()) + uint256(pool.GROWTH_BPS()) * uint256(accepted);
        uint256 penalty = uint256(pool.PENALTY_BPS()) * uint256(writtenOff);

        uint256 expected = penalty >= base ? uint256(pool.FLOOR_BPS()) : base - penalty;
        if (expected < uint256(pool.FLOOR_BPS())) expected = uint256(pool.FLOOR_BPS());
        if (expected > uint256(pool.CAP_BPS())) expected = uint256(pool.CAP_BPS());

        assertEq(uint256(pool.advanceRate(ORG)), expected, "diverged from SC-FP-009");
    }

    // ─────────────────────────────────────────────────────────────────────
    // Demo spine — the exact numbers judges will see (PRD §15.2)
    // ─────────────────────────────────────────────────────────────────────

    /// The rate half of AT-11. On a 25 USDC job a virgin org is priced at 50%,
    /// so rate × customerPayment == 12.50 USDC.
    ///
    /// NOTE: this asserts the RATE term only, deliberately. SC-FP-002 defines
    /// advance = min(maxOperatingBudget, rate × customerPayment), and the demo seed
    /// sets maxOperatingBudget = 6.00 — which would cap the advance at 6.00, not 12.50.
    /// That contradiction is unresolved (see docs/OPEN-SPEC-QUESTIONS.md, SPEC-01);
    /// requestAdvance tests stay unwritten until the team rules on it.
    function test_demoSpine_rateOnTwentyFiveUsdcJob() public view {
        uint256 customerPayment = 25_000_000; // 25.00 USDC, 6dp
        uint256 rateTerm = (customerPayment * pool.advanceRate(ORG)) / 10_000;
        assertEq(rateTerm, 12_500_000, "PRD 15.2: 50% of 25.00 == 12.50 USDC");
    }

    /// The fee the pool is owed on that advance (SC-FP-005, 200 bps).
    /// PRD §15.2: "Float fee (200 bps) | 0.25 USDC", waterfall pays the pool 12.75.
    function test_demoSpine_feeOnAdvance() public view {
        uint256 principal = 12_500_000; // 12.50 USDC
        uint256 fee = (principal * pool.FEE_BPS()) / 10_000;
        assertEq(fee, 250_000, "PRD 15.2: 200 bps of 12.50 == 0.25 USDC");
        assertEq(principal + fee, 12_750_000, "pool receives 12.75 in the waterfall");
    }

    /// AT-13, read straight off the demo script: after the job is accepted the org
    /// re-prices at 55% — "the business just earned cheaper capital by delivering."
    function test_demoSpine_rateRisesAfterAcceptance() public {
        assertEq(pool.advanceRate(ORG), 5000, "before acceptance: 50%");
        pool.setHistory(ORG, 1, 0); // stands in for repayAdvance() incrementing acceptedJobs
        assertEq(pool.advanceRate(ORG), 5500, "after acceptance: 55%");
    }
}
