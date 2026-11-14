// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {Deploy} from "../script/Deploy.s.sol";
import {AuditAnchor} from "../src/AuditAnchor.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

/// script/Deploy.s.sol, run in-process.
///
/// The script's ordering is load-bearing: FloatPool applies an operator grant immediately only
/// before the pool's first setCaps, and queues it for OPERATOR_GRANT_DELAY afterwards. A script
/// that set the caps first would still "succeed", and ship a pool that cannot lend to its own
/// operator for two days. Nothing else would notice, so these tests read the deployed state back.
///
/// Every address here is an obvious placeholder; nothing is broadcast to any network.
contract DeployScriptTest is Test {
    address internal constant OPERATOR  = address(0x09);
    address internal constant NEW_ADMIN = address(0xAD);
    address internal constant CUSTOMER  = address(0xC0);

    // The documented example configuration (docs/MAINNET.md "Choosing the numbers").
    uint256 internal constant MAX_EXPOSURE = 50_000_000; // 50.00 USDC
    uint256 internal constant MAX_ADVANCE  = 10_000_000; // 10.00 USDC
    uint256 internal constant MAX_JOB      = 25_000_000; // 25.00 USDC

    Deploy internal script;
    MockUSDC internal usdc;

    function setUp() public {
        script = new Deploy();
        usdc = new MockUSDC();
    }

    function _cfg(address operator, address newAdmin) internal view returns (Deploy.Config memory) {
        return Deploy.Config({
            usdc: address(usdc),
            maxTotalExposure: MAX_EXPOSURE,
            maxAdvance: MAX_ADVANCE,
            maxJobPayment: MAX_JOB,
            operator: operator,
            newAdmin: newAdmin
        });
    }

    /// The state every Deploy.s.sol deployment must be in the moment the broadcast ends.
    function _assertDeployed(AuditAnchor anchor, JobVault vault, FloatPool pool, address deployer) internal view {
        assertGt(address(anchor).code.length, 0, "anchor deployed");
        assertEq(address(vault.floatPool()), address(pool), "vault -> pool");
        assertEq(address(pool.jobVault()), address(vault), "pool -> vault");
        assertEq(address(pool.usdc()), address(usdc), "pool usdc");
        assertEq(address(vault.usdc()), address(usdc), "vault usdc");

        assertEq(pool.maxTotalExposure(), MAX_EXPOSURE, "maxTotalExposure");
        assertEq(pool.maxAdvance(), MAX_ADVANCE, "maxAdvance");
        assertEq(vault.maxJobPayment(), MAX_JOB, "maxJobPayment");
        assertEq(pool.capRaiseExecutableAt(), 0, "no raise queued");

        // The point of this file: the operator can borrow NOW, not after OPERATOR_GRANT_DELAY.
        assertTrue(pool.allowedOperator(OPERATOR), "operator allowed immediately");
        assertEq(pool.operatorGrantExecutableAt(OPERATOR), 0, "grant applied, not queued");

        assertTrue(pool.depositAllowlistEnabled(), "deposit allowlist on");
        assertTrue(pool.allowedDepositor(deployer), "deployer may seed");
        assertFalse(pool.paused(), "pool not paused");
        assertFalse(vault.paused(), "vault not paused");
        assertEq(pool.admin(), deployer, "deployer is still pool admin");
        assertEq(vault.admin(), deployer, "deployer is still vault admin");
    }

    function test_deploy_allowsOperatorImmediatelyAndStartsHandoff() public {
        (AuditAnchor anchor, JobVault vault, FloatPool pool) = script.deploy(_cfg(OPERATOR, NEW_ADMIN));
        address deployer = pool.admin();
        assertTrue(deployer != address(0) && deployer != NEW_ADMIN && deployer != OPERATOR, "deployer is its own key");
        _assertDeployed(anchor, vault, pool, deployer);

        // Admin handoff started on BOTH contracts, acceptable only after the delay.
        assertEq(pool.pendingAdmin(), NEW_ADMIN, "pool pending admin");
        assertEq(vault.pendingAdmin(), NEW_ADMIN, "vault pending admin");
        assertEq(pool.adminTransferAcceptableAt(), block.timestamp + pool.ADMIN_TRANSFER_DELAY(), "pool delay");
        assertEq(vault.adminTransferAcceptableAt(), block.timestamp + vault.ADMIN_TRANSFER_DELAY(), "vault delay");

        vm.prank(NEW_ADMIN);
        vm.expectRevert(FloatPool.AdminTransferTimelocked.selector);
        pool.acceptAdmin();
    }

    function test_deploy_withoutAdminLeavesNoPendingHandoff() public {
        (AuditAnchor anchor, JobVault vault, FloatPool pool) = script.deploy(_cfg(OPERATOR, address(0)));
        _assertDeployed(anchor, vault, pool, pool.admin());
        assertEq(pool.pendingAdmin(), address(0), "pool: no handoff");
        assertEq(vault.pendingAdmin(), address(0), "vault: no handoff");
    }

    /// End to end on the deployed set, with no admin step in between: the configured operator
    /// draws an advance in the deployment's own block, and nobody else can.
    function test_deploy_operatorCanBorrowAtOnceAndOthersCannot() public {
        (, JobVault vault, FloatPool pool) = script.deploy(_cfg(OPERATOR, address(0)));
        address deployer = pool.admin();

        usdc.mint(deployer, 150_000_000); // 10%-per-org cap: a 10.00 draw needs 100.00 of TVL
        vm.startPrank(deployer);
        usdc.approve(address(pool), type(uint256).max);
        pool.deposit(150_000_000, deployer);
        vm.stopPrank();

        bytes32 jobId = keccak256("deploy-script-job");
        vm.prank(OPERATOR);
        vault.createJob(jobId, CUSTOMER, OPERATOR, 20_000_000, 0, keccak256("t"), uint64(block.timestamp + 7 days));
        usdc.mint(CUSTOMER, 20_000_000);
        vm.startPrank(CUSTOMER);
        usdc.approve(address(vault), type(uint256).max);
        vault.fund(jobId);
        vm.stopPrank();

        vm.prank(OPERATOR);
        assertEq(pool.requestAdvance(jobId), MAX_ADVANCE, "operator borrows in the deploy block");

        address outsider = address(0xBAD);
        bytes32 outsiderJob = keccak256("outsider-job");
        usdc.mint(outsider, 20_000_000);
        vm.startPrank(outsider);
        usdc.approve(address(vault), type(uint256).max);
        vault.createJob(outsiderJob, outsider, outsider, 20_000_000, 0, keccak256("t"), uint64(block.timestamp + 1 days));
        vault.fund(outsiderJob);
        vm.expectRevert(FloatPool.OperatorNotAllowed.selector);
        pool.requestAdvance(outsiderJob);
        vm.stopPrank();
    }

    function test_deploy_requiresAnOperator() public {
        vm.expectRevert(bytes("SNAPFALL_OPERATOR must be set"));
        script.deploy(_cfg(address(0), NEW_ADMIN));
    }

    function test_deploy_rejectsBadCaps() public {
        Deploy.Config memory cfg = _cfg(OPERATOR, address(0));
        cfg.maxAdvance = MAX_EXPOSURE + 1;
        vm.expectRevert(bytes("per-advance cap cannot exceed the total ceiling"));
        script.deploy(cfg);

        cfg = _cfg(OPERATOR, address(0));
        cfg.maxJobPayment = 0;
        vm.expectRevert(bytes("SNAPFALL_MAX_JOB_PAYMENT must be > 0"));
        script.deploy(cfg);
    }

    /// run() itself, through the environment, so a swapped or renamed variable is caught too.
    /// Both cases live in one test on purpose: vm.setEnv is process-wide, and tests run in
    /// parallel, so a second test writing the same variables could race this one. Every variable
    /// run() reads is set here, so nothing from the caller's shell leaks in.
    function test_run_readsTheEnvironment() public {
        vm.setEnv("ARC_USDC_ADDRESS", vm.toString(address(usdc)));
        vm.setEnv("SNAPFALL_MAX_TOTAL_EXPOSURE", vm.toString(MAX_EXPOSURE));
        vm.setEnv("SNAPFALL_MAX_ADVANCE", vm.toString(MAX_ADVANCE));
        vm.setEnv("SNAPFALL_MAX_JOB_PAYMENT", vm.toString(MAX_JOB));
        vm.setEnv("SNAPFALL_ADMIN", vm.toString(NEW_ADMIN));

        // vm.setEnv cannot unset, so "missing" is the zero address, which the script also refuses.
        vm.setEnv("SNAPFALL_OPERATOR", vm.toString(address(0)));
        vm.expectRevert(bytes("SNAPFALL_OPERATOR must be set"));
        script.run();

        vm.setEnv("SNAPFALL_OPERATOR", vm.toString(OPERATOR));
        (AuditAnchor anchor, JobVault vault, FloatPool pool) = script.run();
        _assertDeployed(anchor, vault, pool, pool.admin());
        assertEq(pool.pendingAdmin(), NEW_ADMIN, "SNAPFALL_ADMIN starts the pool handoff");
        assertEq(vault.pendingAdmin(), NEW_ADMIN, "SNAPFALL_ADMIN starts the vault handoff");
    }
}
