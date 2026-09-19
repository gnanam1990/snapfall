// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

/// Mainnet capital controls — the preconditions a public deployment has to meet.
///
/// The existing caps (ORG_EXPOSURE_CAP_BPS, UTILIZATION_CAP_BPS) are percentages of TVL, so
/// they bound the SHAPE of the book but not its SIZE: more capital in the pool means
/// proportionally more capital at risk, without limit. On a testnet that costs nothing. On
/// mainnet it is an unbounded exposure, so these tests pin three absolute, contract-enforced
/// controls that hold regardless of how the daemon, dashboard or operator behave:
///
///   1. an absolute ceiling on total outstanding principal, and on any single advance;
///   2. a pause that stops new risk WITHOUT trapping money that is already in;
///   3. an allowlist on deposits, so a public address cannot push third-party capital in.
///
/// The pause tests are the important half. A pause that blocks withdraw, repay, settlement or
/// refund would convert an emergency stop into a fund freeze — strictly worse than no pause at
/// all. Every exit path is asserted to stay open while paused.
contract CapsTest is Test {
    JobVault internal vault;
    FloatPool internal pool;
    MockUSDC internal usdc;

    address internal constant ADMIN    = address(0xA0);
    address internal constant CUSTOMER = address(0xC0);
    address internal constant OPERATOR = address(0x09);
    address internal constant STRANGER = address(0xBAD);
    address internal constant LP       = address(0x1D);

    bytes32 internal constant TERMS = keccak256("terms");

    uint256 internal constant PAYMENT   = 25_000_000;   // 25.00 USDC
    uint256 internal constant BUDGET    =  6_000_000;
    uint256 internal constant POOL_SEED = 150_000_000;  // 150.00 USDC

    uint256 internal constant MAX_EXPOSURE = 50_000_000; // 50.00 USDC
    uint256 internal constant MAX_ADVANCE  = 20_000_000; // 20.00 USDC
    uint256 internal constant MAX_JOB      = 30_000_000; // 30.00 USDC

    event PauseSet(bool paused);
    event CapsSet(uint256 maxTotalExposure, uint256 maxAdvance);
    event DepositAllowlistSet(bool enabled);
    event DepositorAllowed(address indexed depositor, bool allowed);
    event MaxJobPaymentSet(uint256 maxJobPayment);

    function setUp() public {
        usdc = new MockUSDC();

        vm.startPrank(ADMIN);
        vault = new JobVault(IERC20(address(usdc)));
        pool = new FloatPool(IERC20(address(usdc)));
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));
        vm.stopPrank();

        usdc.mint(CUSTOMER, PAYMENT * 10);
        vm.prank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
    }

    // ── helpers ──────────────────────────────────────────────────────────

    /// Open the contracts to the degree a normal test needs: caps set, LP allowed to deposit.
    function _openForBusiness() internal {
        vm.startPrank(ADMIN);
        pool.setCaps(MAX_EXPOSURE, MAX_ADVANCE);
        pool.setDepositorAllowed(LP, true);
        vault.setMaxJobPayment(MAX_JOB);
        vm.stopPrank();
    }

    function _seedPool(uint256 assets) internal {
        usdc.mint(LP, assets);
        vm.startPrank(LP);
        usdc.approve(address(pool), type(uint256).max);
        pool.deposit(assets, LP);
        vm.stopPrank();
    }

    function _createAndFund(bytes32 jobId, uint256 payment) internal {
        vm.prank(ADMIN);
        vault.createJob(jobId, CUSTOMER, OPERATOR, payment, BUDGET, TERMS, uint64(block.timestamp + 7 days));
        vm.prank(CUSTOMER);
        vault.fund(jobId);
    }

    // ─────────────────────────────────────────────────────────────────────
    // 1. Absolute exposure ceiling
    // ─────────────────────────────────────────────────────────────────────

    /// Fail closed. A pool that has never been given a cap lends nothing — forgetting to set
    /// one cannot silently mean "unlimited", which is the failure this whole file exists for.
    function test_requestAdvance_revertsWhenCapNeverSet() public {
        vm.prank(ADMIN);
        pool.setDepositorAllowed(LP, true);
        vm.prank(ADMIN);
        vault.setMaxJobPayment(MAX_JOB);
        _seedPool(POOL_SEED);
        _createAndFund(keccak256("j1"), PAYMENT);

        vm.prank(OPERATOR);
        vm.expectRevert(FloatPool.CapNotSet.selector);
        pool.requestAdvance(keccak256("j1"));
    }

    function test_requestAdvance_revertsWhenSingleAdvanceExceedsMaxAdvance() public {
        _openForBusiness();
        _seedPool(POOL_SEED);

        // 25.00 payment at the 50% base rate draws 12.50, which is inside MAX_ADVANCE.
        // Tighten the per-advance cap below that and the same draw must be refused.
        vm.prank(ADMIN);
        pool.setCaps(MAX_EXPOSURE, 10_000_000); // 10.00 USDC

        _createAndFund(keccak256("j2"), PAYMENT);

        vm.prank(OPERATOR);
        vm.expectRevert(FloatPool.AdvanceTooLarge.selector);
        pool.requestAdvance(keccak256("j2"));
    }

    function test_requestAdvance_revertsWhenTotalExposureWouldBreachCeiling() public {
        _openForBusiness();
        _seedPool(POOL_SEED);

        // Ceiling of 20.00 total. The first 12.50 advance fits; the second would reach 25.00.
        vm.prank(ADMIN);
        pool.setCaps(20_000_000, MAX_ADVANCE);

        _createAndFund(keccak256("j3a"), PAYMENT);
        vm.prank(OPERATOR);
        pool.requestAdvance(keccak256("j3a"));
        assertEq(pool.totalOutstanding(), 12_500_000, "first advance should land");

        _createAndFund(keccak256("j3b"), PAYMENT);
        vm.prank(OPERATOR);
        vm.expectRevert(FloatPool.ExposureCapExceeded.selector);
        pool.requestAdvance(keccak256("j3b"));

        assertEq(pool.totalOutstanding(), 12_500_000, "refused advance must not move the book");
    }

    /// The point of an ABSOLUTE cap: the percentage caps scale with TVL and would not bind
    /// here, so only the absolute ceiling stands between a large pool and a large loss.
    function test_exposureCeilingBindsWhereThePercentageCapsDoNot() public {
        _openForBusiness();
        _seedPool(100_000_000_000); // 100,000 USDC — 10% per-org is 10,000, far above any draw

        vm.prank(ADMIN);
        pool.setCaps(5_000_000, MAX_ADVANCE); // 5.00 USDC ceiling

        _createAndFund(keccak256("j4"), PAYMENT);

        // 12.50 is ~0.0125% of TVL, so ORG_EXPOSURE_CAP_BPS and UTILIZATION_CAP_BPS are
        // nowhere near binding. The absolute ceiling is the only thing that refuses it.
        vm.prank(OPERATOR);
        vm.expectRevert(FloatPool.ExposureCapExceeded.selector);
        pool.requestAdvance(keccak256("j4"));
    }

    function test_setCaps_isAdminOnly() public {
        vm.prank(STRANGER);
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.setCaps(MAX_EXPOSURE, MAX_ADVANCE);
    }

    function test_setCaps_emitsCapsSet() public {
        vm.expectEmit(false, false, false, true, address(pool));
        emit CapsSet(MAX_EXPOSURE, MAX_ADVANCE);
        vm.prank(ADMIN);
        pool.setCaps(MAX_EXPOSURE, MAX_ADVANCE);

        assertEq(pool.maxTotalExposure(), MAX_EXPOSURE);
        assertEq(pool.maxAdvance(), MAX_ADVANCE);
    }

    // ─────────────────────────────────────────────────────────────────────
    // 2. Pause — stops new risk, never traps money
    // ─────────────────────────────────────────────────────────────────────

    function test_pause_blocksDeposit() public {
        _openForBusiness();
        usdc.mint(LP, POOL_SEED);
        vm.prank(LP);
        usdc.approve(address(pool), type(uint256).max);

        vm.prank(ADMIN);
        pool.setPaused(true);

        vm.prank(LP);
        vm.expectRevert(FloatPool.EnforcedPause.selector);
        pool.deposit(POOL_SEED, LP);
    }

    function test_pause_blocksRequestAdvance() public {
        _openForBusiness();
        _seedPool(POOL_SEED);
        _createAndFund(keccak256("j5"), PAYMENT);

        vm.prank(ADMIN);
        pool.setPaused(true);

        vm.prank(OPERATOR);
        vm.expectRevert(FloatPool.EnforcedPause.selector);
        pool.requestAdvance(keccak256("j5"));
    }

    /// An emergency stop that locks LP capital in is worse than none. Withdrawal of idle
    /// capital must survive a pause.
    function test_pause_doesNotBlockWithdraw() public {
        _openForBusiness();
        _seedPool(POOL_SEED);

        vm.prank(ADMIN);
        pool.setPaused(true);

        vm.prank(LP);
        pool.withdraw(POOL_SEED, LP, LP);

        assertEq(usdc.balanceOf(LP), POOL_SEED, "LP must be able to exit while paused");
        assertEq(pool.totalAssets(), 0);
    }

    /// A settlement in flight when the pause lands must still complete: the pool gets repaid
    /// and the operator gets the remainder. Pausing mid-job must not strand escrow.
    function test_pause_doesNotBlockSettlement() public {
        _openForBusiness();
        _seedPool(POOL_SEED);

        bytes32 job = keccak256("j6");
        _createAndFund(job, PAYMENT);
        vm.prank(OPERATOR);
        pool.requestAdvance(job);
        vm.prank(OPERATOR);
        vault.startWork(job);
        vm.prank(OPERATOR);
        vault.submitDelivery(job, keccak256("delivered"));

        vm.startPrank(ADMIN);
        pool.setPaused(true);
        vault.setPaused(true);
        vm.stopPrank();

        vm.prank(CUSTOMER);
        vault.acceptDelivery(job);

        (, , bool open) = pool.openAdvanceOf(job);
        assertFalse(open, "advance must settle while both contracts are paused");
        assertEq(uint8(vault.jobStatus(job)), uint8(JobVault.JobStatus.Accepted));
    }

    /// The customer's escape hatch has to survive a pause too.
    function test_pause_doesNotBlockRefund() public {
        _openForBusiness();
        _seedPool(POOL_SEED);

        bytes32 job = keccak256("j7");
        _createAndFund(job, PAYMENT);

        vm.startPrank(ADMIN);
        pool.setPaused(true);
        vault.setPaused(true);
        vm.stopPrank();

        uint256 before = usdc.balanceOf(CUSTOMER);
        vm.prank(ADMIN);
        vault.refund(job);

        assertEq(usdc.balanceOf(CUSTOMER) - before, PAYMENT, "customer must be made whole while paused");
    }

    function test_setPaused_isAdminOnly() public {
        vm.prank(STRANGER);
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.setPaused(true);

        vm.prank(STRANGER);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.setPaused(true);
    }

    function test_unpause_restoresNormalOperation() public {
        _openForBusiness();
        _seedPool(POOL_SEED);
        _createAndFund(keccak256("j8"), PAYMENT);

        vm.prank(ADMIN);
        pool.setPaused(true);
        vm.prank(ADMIN);
        pool.setPaused(false);

        vm.prank(OPERATOR);
        uint256 principal = pool.requestAdvance(keccak256("j8"));
        assertEq(principal, 12_500_000, "advance should issue normally after unpause");
    }

    function test_setPaused_emitsPauseSet() public {
        vm.expectEmit(false, false, false, true, address(pool));
        emit PauseSet(true);
        vm.prank(ADMIN);
        pool.setPaused(true);
        assertTrue(pool.paused());
    }

    // ─────────────────────────────────────────────────────────────────────
    // 3. Deposit allowlist — no public pooled liquidity
    // ─────────────────────────────────────────────────────────────────────

    function test_deposit_rejectsAddressNotOnTheAllowlist() public {
        _openForBusiness();
        usdc.mint(STRANGER, POOL_SEED);
        vm.startPrank(STRANGER);
        usdc.approve(address(pool), type(uint256).max);
        vm.expectRevert(FloatPool.DepositorNotAllowed.selector);
        pool.deposit(POOL_SEED, STRANGER);
        vm.stopPrank();
    }

    function test_deployerIsAllowlistedByDefault() public {
        assertTrue(pool.allowedDepositor(ADMIN), "the deployer must be able to seed its own pool");
        assertTrue(pool.depositAllowlistEnabled(), "the allowlist must default to ON");
    }

    function test_deposit_acceptsAllowlistedAddress() public {
        _openForBusiness();
        _seedPool(POOL_SEED);
        assertEq(pool.totalAssets(), POOL_SEED);
        assertEq(pool.sharesOf(LP), POOL_SEED);
    }

    function test_disablingTheAllowlistOpensDepositsToAnyone() public {
        _openForBusiness();
        vm.prank(ADMIN);
        pool.setDepositAllowlistEnabled(false);

        usdc.mint(STRANGER, POOL_SEED);
        vm.startPrank(STRANGER);
        usdc.approve(address(pool), type(uint256).max);
        pool.deposit(POOL_SEED, STRANGER);
        vm.stopPrank();

        assertEq(pool.sharesOf(STRANGER), POOL_SEED, "an open pool is a deliberate choice, not the default");
    }

    function test_allowlistSetters_areAdminOnly() public {
        vm.prank(STRANGER);
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.setDepositorAllowed(STRANGER, true);

        vm.prank(STRANGER);
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        pool.setDepositAllowlistEnabled(false);
    }

    function test_setDepositorAllowed_emitsDepositorAllowed() public {
        vm.expectEmit(true, false, false, true, address(pool));
        emit DepositorAllowed(LP, true);
        vm.prank(ADMIN);
        pool.setDepositorAllowed(LP, true);
    }

    // ─────────────────────────────────────────────────────────────────────
    // 4. JobVault — per-job ceiling and pause
    // ─────────────────────────────────────────────────────────────────────

    function test_createJob_revertsWhenMaxJobPaymentNeverSet() public {
        vm.prank(ADMIN);
        vm.expectRevert(JobVault.CapNotSet.selector);
        vault.createJob(keccak256("j9"), CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, uint64(block.timestamp + 1 days));
    }

    function test_createJob_revertsAboveTheJobCeiling() public {
        _openForBusiness();
        vm.prank(ADMIN);
        vm.expectRevert(JobVault.JobTooLarge.selector);
        vault.createJob(
            keccak256("j10"), CUSTOMER, OPERATOR, MAX_JOB + 1, BUDGET, TERMS, uint64(block.timestamp + 1 days)
        );
    }

    function test_createJob_acceptsExactlyTheCeiling() public {
        _openForBusiness();
        vm.prank(ADMIN);
        vault.createJob(keccak256("j11"), CUSTOMER, OPERATOR, MAX_JOB, BUDGET, TERMS, uint64(block.timestamp + 1 days));
        (, , uint256 payment, , , , , , ) = vault.jobs(keccak256("j11"));
        assertEq(payment, MAX_JOB, "the ceiling itself must be allowed, not rejected");
    }

    function test_pause_blocksCreateJob() public {
        _openForBusiness();
        vm.prank(ADMIN);
        vault.setPaused(true);

        vm.prank(ADMIN);
        vm.expectRevert(JobVault.EnforcedPause.selector);
        vault.createJob(keccak256("j12"), CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, uint64(block.timestamp + 1 days));
    }

    function test_pause_blocksFund() public {
        _openForBusiness();
        vm.prank(ADMIN);
        vault.createJob(keccak256("j13"), CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, uint64(block.timestamp + 1 days));

        vm.prank(ADMIN);
        vault.setPaused(true);

        vm.prank(CUSTOMER);
        vm.expectRevert(JobVault.EnforcedPause.selector);
        vault.fund(keccak256("j13"));
    }

    /// A job created but never funded must still be cancellable while paused, or the customer
    /// is left with a dangling obligation they cannot clear.
    function test_pause_doesNotBlockCancel() public {
        _openForBusiness();
        vm.prank(ADMIN);
        vault.createJob(keccak256("j14"), CUSTOMER, OPERATOR, PAYMENT, BUDGET, TERMS, uint64(block.timestamp + 1 days));

        vm.prank(ADMIN);
        vault.setPaused(true);

        vm.prank(CUSTOMER);
        vault.cancel(keccak256("j14"));
        assertEq(uint8(vault.jobStatus(keccak256("j14"))), uint8(JobVault.JobStatus.Cancelled));
    }

    function test_setMaxJobPayment_isAdminOnly() public {
        vm.prank(STRANGER);
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vault.setMaxJobPayment(MAX_JOB);
    }

    function test_setMaxJobPayment_emitsMaxJobPaymentSet() public {
        vm.expectEmit(false, false, false, true, address(vault));
        emit MaxJobPaymentSet(MAX_JOB);
        vm.prank(ADMIN);
        vault.setMaxJobPayment(MAX_JOB);
        assertEq(vault.maxJobPayment(), MAX_JOB);
    }

    // ─────────────────────────────────────────────────────────────────────
    // 5. The controls survive a hostile operator
    // ─────────────────────────────────────────────────────────────────────

    /// The plan's actual requirement: the ceiling holds even if everything off-chain misbehaves.
    /// The operator here does exactly what a compromised daemon would — draw repeatedly, as fast
    /// as it can, against freshly funded jobs — and the contract stops it at the ceiling.
    function test_ceilingHoldsAgainstRepeatedDrawsByTheOperator() public {
        _openForBusiness();
        // 400.00 USDC, deliberately deep. At 150.00 the 10%-per-org percentage cap would stop
        // the second draw and this test would be asserting the OLD control, not the new one.
        // A big pool is exactly the condition under which the percentage caps stop protecting
        // anything, so it is the condition the absolute ceiling has to be proven under.
        _seedPool(400_000_000);

        vm.prank(ADMIN);
        pool.setCaps(30_000_000, MAX_ADVANCE); // 30.00 ceiling; each draw is 12.50

        uint256 landed;
        for (uint256 i = 0; i < 10; i++) {
            bytes32 job = keccak256(abi.encodePacked("spam", i));
            _createAndFund(job, PAYMENT);
            vm.prank(OPERATOR);
            try pool.requestAdvance(job) returns (uint256) {
                landed++;
            } catch {}
        }

        assertEq(landed, 2, "only two 12.50 draws fit under a 30.00 ceiling");
        assertLe(pool.totalOutstanding(), 30_000_000, "outstanding must never exceed the ceiling");
    }
}
