// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";
import {FloatPoolHarness} from "./mocks/FloatPoolHarness.sol";

/// SC-FP-001..006 — advance issuance against a Funded job.
///
/// SPEC-01 RULING (19 Jul 2026): advance = advanceRate(org) × customerPayment.
/// The min(maxOperatingBudget, …) term is gone; maxOperatingBudget is the SC-JV-003
/// spend bound only and has no bearing on borrowing capacity.
contract AdvanceTest is Test {
    JobVault internal vault;
    FloatPoolHarness internal pool;
    MockUSDC internal usdc;

    address internal constant ADMIN    = address(0xA0);
    address internal constant CUSTOMER = address(0xC0);
    address internal constant OPERATOR = address(0x09);   // org treasury
    address internal constant STRANGER = address(0xBAD);
    address internal constant LP       = address(0x1D);

    bytes32 internal constant JOB   = keccak256("job_104");
    bytes32 internal constant TERMS = keccak256("competitor analysis");

    uint256 internal constant PAYMENT = 25_000_000;  // 25.00 USDC (PRD §15.2)
    uint256 internal constant BUDGET  =  6_000_000;  //  6.00 USDC — spend bound, NOT a borrow cap

    /// 150.00 USDC. NOT the PRD's 100.00 demo seed — see test_exposureCap_rejectsDemoSeedOf100
    /// and docs/OPEN-SPEC-QUESTIONS.md SPEC-06: a 12.50 advance needs TVL ≥ 125.00 to stay
    /// inside the 10% per-org exposure cap.
    uint256 internal constant POOL_SEED = 150_000_000;

    event AdvanceIssued(bytes32 indexed jobId, address indexed org, uint256 principal, uint256 fee, uint16 rateBps);

    function setUp() public {
        usdc = new MockUSDC();

        vm.startPrank(ADMIN);
        vault = new JobVault(IERC20(address(usdc)));
        pool = new FloatPoolHarness(IERC20(address(usdc)));
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));
        vm.stopPrank();

        _seedPool(POOL_SEED);

        usdc.mint(CUSTOMER, PAYMENT);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
    }

    // ── helpers ──────────────────────────────────────────────────────────

    function _seedPool(uint256 assets) internal {
        usdc.mint(LP, assets);
        vm.startPrank(LP);
        usdc.approve(address(pool), type(uint256).max);
        pool.deposit(assets, LP);
        vm.stopPrank();
    }

    function _createAndFund(bytes32 jobId, uint256 payment, uint256 budget) internal {
        vm.prank(ADMIN);
        vault.createJob(jobId, CUSTOMER, OPERATOR, payment, budget, TERMS, uint64(block.timestamp + 7 days));
        vm.prank(CUSTOMER);
        vault.fund(jobId);
    }

    function _fundDemoJob() internal {
        _createAndFund(JOB, PAYMENT, BUDGET);
    }

    // ─────────────────────────────────────────────────────────────────────
    // The demo numbers (AT-11, PRD §15.2) — written first
    // ─────────────────────────────────────────────────────────────────────

    /// AT-11: "Funded 25 USDC job at 50% → requestAdvance transfers exactly 12.50 to treasury."
    /// This is the 0:40 "snap" beat. Treasury goes 0 → 12.50.
    function test_requestAdvance_demoNumbers() public {
        _fundDemoJob();
        assertEq(usdc.balanceOf(OPERATOR), 0, "treasury starts at zero - the whole thesis");

        vm.expectEmit(true, true, true, true);
        emit AdvanceIssued(JOB, OPERATOR, 12_500_000, 250_000, 5000);

        vm.prank(OPERATOR);
        uint256 amount = pool.requestAdvance(JOB);

        assertEq(amount, 12_500_000, "50% of 25.00 == 12.50 USDC");
        assertEq(usdc.balanceOf(OPERATOR), 12_500_000, "treasury credited 12.50");

        (uint256 principal, uint256 fee, bool open) = pool.openAdvanceOf(JOB);
        assertEq(principal, 12_500_000, "principal 12.50");
        assertEq(fee, 250_000, "fee 0.25 == 200 bps of principal (SC-FP-005)");
        assertTrue(open, "advance is open until the waterfall repays it");
    }

    /// SPEC-01, stated as a test: maxOperatingBudget (6.00) is far below the advance (12.50)
    /// and must not constrain it. Under the old min() formula this returned 6.00.
    function test_requestAdvance_ignoresMaxOperatingBudget() public {
        _fundDemoJob();

        vm.prank(OPERATOR);
        uint256 amount = pool.requestAdvance(JOB);

        assertEq(amount, 12_500_000, "SPEC-01: the 6.00 spend bound must not cap the advance");
        assertGt(amount, BUDGET, "advance deliberately exceeds maxOperatingBudget");
    }

    /// The rate is read live, so an org with history borrows more against the same job.
    function test_requestAdvance_usesCurrentRate() public {
        bytes32 job2 = keccak256("job_205");
        usdc.mint(CUSTOMER, PAYMENT);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
        _createAndFund(job2, PAYMENT, BUDGET);

        // One accepted job in this org's history: 50% -> 55%.
        pool.setHistory(OPERATOR, 1, 0);
        assertEq(pool.advanceRate(OPERATOR), 5500, "history seeded to 1 accepted job");

        vm.prank(OPERATOR);
        uint256 amount = pool.requestAdvance(job2);
        assertEq(amount, 13_750_000, "55% of 25.00 == 13.75 USDC");
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-001 — the vault is the source of truth, never the caller
    // ─────────────────────────────────────────────────────────────────────

    function test_requestAdvance_revertsIfJobNotFunded() public {
        vm.prank(ADMIN);
        vault.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, uint64(block.timestamp + 7 days));

        vm.expectRevert(FloatPool.JobNotFunded.selector);
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);
    }

    function test_requestAdvance_revertsForUnknownJob() public {
        vm.expectRevert(FloatPool.JobNotFunded.selector);
        vm.prank(OPERATOR);
        pool.requestAdvance(keccak256("never_created"));
    }

    /// Once work starts the job leaves Funded, and SC-FP-001 admits only Funded.
    function test_requestAdvance_revertsAfterWorkStarted() public {
        _fundDemoJob();
        vm.prank(OPERATOR);
        vault.startWork(JOB);

        vm.expectRevert(FloatPool.JobNotFunded.selector);
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-004 / ADR-009 — only the registered treasury borrows, and only it is paid
    // ─────────────────────────────────────────────────────────────────────

    /// AT-15, contract half: an agent-originated or otherwise non-treasury advance is rejected.
    function test_requestAdvance_revertsForAnyNonTreasury() public {
        _fundDemoJob();

        address[3] memory impostors = [CUSTOMER, ADMIN, STRANGER];
        for (uint256 i = 0; i < impostors.length; i++) {
            vm.expectRevert(FloatPool.NotTreasury.selector);
            vm.prank(impostors[i]);
            pool.requestAdvance(JOB);
        }
        assertEq(usdc.balanceOf(address(pool)), POOL_SEED, "pool untouched by rejected callers");
    }

    /// SC-FP-004: funds land on the treasury address read from the vault. The caller cannot
    /// nominate a destination, so there is no path to an agent wallet.
    function test_requestAdvance_paysOnlyTheRegisteredTreasury() public {
        _fundDemoJob();

        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);

        assertEq(usdc.balanceOf(OPERATOR), 12_500_000, "treasury paid");
        assertEq(usdc.balanceOf(CUSTOMER), 0, "customer not paid");
        assertEq(usdc.balanceOf(STRANGER), 0, "stranger not paid");
        assertEq(usdc.balanceOf(address(vault)), PAYMENT, "escrow untouched by the advance");
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-003 — exactly one advance per job
    // ─────────────────────────────────────────────────────────────────────

    function test_requestAdvance_revertsOnDuplicate() public {
        _fundDemoJob();

        vm.startPrank(OPERATOR);
        pool.requestAdvance(JOB);

        vm.expectRevert(FloatPool.DuplicateAdvance.selector);
        pool.requestAdvance(JOB);
        vm.stopPrank();

        assertEq(usdc.balanceOf(OPERATOR), 12_500_000, "treasury credited exactly once");
        assertEq(pool.totalOutstanding(), 12_500_000, "one open principal");
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-006 — exposure and utilization caps
    // ─────────────────────────────────────────────────────────────────────

    /// SPEC-06, pinned as a test. The PRD §15.2 demo seed of 100.00 USDC cannot support the
    /// 12.50 advance the demo script requires: 12.50 / 100.00 = 12.5% > the 10% per-org cap.
    /// TVL must be ≥ 125.00. If this test ever starts passing, someone raised the cap.
    function test_exposureCap_rejectsDemoSeedOf100() public {
        MockUSDC token = new MockUSDC();
        vm.startPrank(ADMIN);
        JobVault v = new JobVault(IERC20(address(token)));
        FloatPool p = new FloatPool(IERC20(address(token)));
        v.wireFloatPool(address(p));
        p.wireJobVault(address(v));
        vm.stopPrank();

        // The PRD's demo pool seed.
        token.mint(LP, 100_000_000);
        vm.startPrank(LP);
        token.approve(address(p), type(uint256).max);
        p.deposit(100_000_000, LP);
        vm.stopPrank();

        token.mint(CUSTOMER, PAYMENT);
        vm.startPrank(CUSTOMER);
        token.approve(address(v), type(uint256).max);
        vm.stopPrank();
        vm.prank(ADMIN);
        v.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, uint64(block.timestamp + 7 days));
        vm.prank(CUSTOMER);
        v.fund(JOB);

        vm.expectRevert(FloatPool.CapExceeded.selector);
        vm.prank(OPERATOR);
        p.requestAdvance(JOB);
    }

    /// 125.00 TVL is the exact boundary: 12.50 / 125.00 == 10%, inclusive.
    function test_exposureCap_admitsExactlyTenPercent() public {
        MockUSDC token = new MockUSDC();
        vm.startPrank(ADMIN);
        JobVault v = new JobVault(IERC20(address(token)));
        FloatPool p = new FloatPool(IERC20(address(token)));
        v.wireFloatPool(address(p));
        p.wireJobVault(address(v));
        vm.stopPrank();

        token.mint(LP, 125_000_000);
        vm.startPrank(LP);
        token.approve(address(p), type(uint256).max);
        p.deposit(125_000_000, LP);
        vm.stopPrank();

        token.mint(CUSTOMER, PAYMENT);
        vm.startPrank(CUSTOMER);
        token.approve(address(v), type(uint256).max);
        vm.stopPrank();
        vm.prank(ADMIN);
        v.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, uint64(block.timestamp + 7 days));
        vm.prank(CUSTOMER);
        v.fund(JOB);

        vm.prank(OPERATOR);
        uint256 amount = p.requestAdvance(JOB);
        assertEq(amount, 12_500_000, "exactly 10% exposure is allowed");
    }

    /// A second job for the SAME org accumulates exposure and trips the cap.
    function test_exposureCap_accumulatesAcrossJobsForOneOrg() public {
        _fundDemoJob();
        vm.prank(OPERATOR);
        pool.requestAdvance(JOB); // 12.50 of 150.00 == 8.33%

        bytes32 job2 = keccak256("job_205");
        usdc.mint(CUSTOMER, PAYMENT);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
        _createAndFund(job2, PAYMENT, BUDGET);

        // A second 12.50 would take the org to 25.00 of 150.00 == 16.7% > 10%.
        vm.expectRevert(FloatPool.CapExceeded.selector);
        vm.prank(OPERATOR);
        pool.requestAdvance(job2);
    }

    /// Utilization is global. Enough distinct orgs, each inside the per-org cap, must still
    /// stop at 80% of TVL.
    function test_utilizationCap_stopsGlobalLendingAtEightyPercent() public {
        // 10 orgs × 10% each would be 100% utilization; the global cap must bite at 80%.
        uint256 issued;
        for (uint256 i = 1; i <= 10; i++) {
            address org = address(uint160(0x1000 + i));
            bytes32 job = keccak256(abi.encode("job", i));
            address cust = address(uint160(0x2000 + i));

            usdc.mint(cust, 30_000_000);
            vm.prank(cust);
            usdc.approve(address(vault), type(uint256).max);

            vm.prank(ADMIN);
            vault.createJob(job, cust, org, 30_000_000, BUDGET, TERMS, uint64(block.timestamp + 7 days));
            vm.prank(cust);
            vault.fund(job);

            // 50% of 30.00 = 15.00 = exactly 10% of the 150.00 pool.
            vm.prank(org);
            try pool.requestAdvance(job) returns (uint256 amt) {
                issued += amt;
            } catch {
                break;
            }
        }

        assertEq(issued, 120_000_000, "lending stops at 120.00 == 80% of 150.00 TVL");
        assertEq(pool.totalOutstanding(), 120_000_000, "utilization pinned at the cap");
    }

    function test_requestAdvance_revertsWhenPoolLacksLiquidity() public {
        MockUSDC token = new MockUSDC();
        vm.startPrank(ADMIN);
        JobVault v = new JobVault(IERC20(address(token)));
        FloatPool p = new FloatPool(IERC20(address(token)));
        v.wireFloatPool(address(p));
        p.wireJobVault(address(v));
        vm.stopPrank();

        token.mint(CUSTOMER, PAYMENT);
        vm.startPrank(CUSTOMER);
        token.approve(address(v), type(uint256).max);
        vm.stopPrank();
        vm.prank(ADMIN);
        v.createJob(JOB, CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, uint64(block.timestamp + 7 days));
        vm.prank(CUSTOMER);
        v.fund(JOB);

        // Pool has no LP capital at all.
        vm.expectRevert(FloatPool.InsufficientLiquidity.selector);
        vm.prank(OPERATOR);
        p.requestAdvance(JOB);
    }

    // ─────────────────────────────────────────────────────────────────────
    // Wiring
    // ─────────────────────────────────────────────────────────────────────

    function test_requestAdvance_revertsWhenPoolUnwired() public {
        FloatPool orphan = new FloatPool(IERC20(address(usdc)));
        vm.expectRevert(FloatPool.NotWired.selector);
        vm.prank(OPERATOR);
        orphan.requestAdvance(JOB);
    }

    // ─────────────────────────────────────────────────────────────────────
    // Solvency — the invariant that replaces the removed min()
    // ─────────────────────────────────────────────────────────────────────

    /// SPEC-01's solvency argument, asserted rather than reasoned about.
    ///
    /// The waterfall must never owe the pool more than the escrow holds. The worst case is
    /// CAP_BPS (85%) plus the 200 bps fee on that principal = 86.7% of customerPayment, so
    /// there is ~13% headroom at the cap. Fuzzed across the full rate range (every reachable
    /// org history) and every plausible job size.
    function testFuzz_advance_neverExceedsEscrow(
        uint256 customerPayment,
        uint32 accepted,
        uint32 writtenOff
    ) public {
        customerPayment = bound(customerPayment, 1_000_000, 1_000_000_000_000); // 1.00 .. 1,000,000.00

        address org = address(0xF00D);
        pool.setHistory(org, accepted, writtenOff);
        uint16 rateBps = pool.advanceRate(org);

        uint256 principal = (customerPayment * uint256(rateBps)) / 10_000;
        uint256 fee = (principal * uint256(pool.FEE_BPS())) / 10_000;

        assertLe(principal + fee, customerPayment, "escrow must always cover principal + fee");
        // 85% cap x 1.02 fee multiplier = 86.7% worst case, so ~13% headroom always remains.
        assertLe((principal + fee) * 10_000, customerPayment * 8_700, "owed stays under 87% of escrow");
    }

    /// The same solvency invariant driven through the LIVE contract: fund a job of a fuzzed
    /// size, draw the advance, and assert the escrow still covers what the waterfall will owe.
    function testFuzz_liveAdvance_isCoveredByEscrow(uint256 customerPayment, uint32 accepted) public {
        // Keep the draw inside the 10% per-org exposure cap against the 150.00 seed.
        customerPayment = bound(customerPayment, 1_000_000, 17_000_000);
        accepted = uint32(bound(accepted, 0, 7)); // 0 -> 50%, 7 -> capped at 85%

        pool.setHistory(OPERATOR, accepted, 0);

        bytes32 job = keccak256(abi.encode("fuzz", customerPayment, accepted));
        usdc.mint(CUSTOMER, customerPayment);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
        _createAndFund(job, customerPayment, BUDGET);

        vm.prank(OPERATOR);
        pool.requestAdvance(job);

        (uint256 principal, uint256 fee, bool open) = pool.openAdvanceOf(job);
        assertTrue(open, "advance opened");
        assertLe(principal + fee, usdc.balanceOf(address(vault)), "escrow covers the repayment leg");
    }

    /// The same invariant against the live contract on the demo job, end to end.
    function test_advance_leavesEscrowHeadroom() public {
        _fundDemoJob();

        vm.prank(OPERATOR);
        pool.requestAdvance(JOB);

        (uint256 principal, uint256 fee, ) = pool.openAdvanceOf(JOB);
        uint256 owed = principal + fee;

        assertEq(owed, 12_750_000, "pool is owed 12.75 (PRD 15.2)");
        assertLe(owed, PAYMENT, "escrow of 25.00 covers it");
        assertEq(PAYMENT - owed, 12_250_000, "operator's share is 12.25");
    }
}
