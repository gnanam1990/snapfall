// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";

/// SPEC-04 — set-once wiring between JobVault and FloatPool.
///
/// The waterfall (SC-JV-009) and SC-FP-010's "callable only by the registered JobVault"
/// both depend on these pointers. Rebinding them mid-flight would let an admin redirect
/// repayments, so both setters are one-shot and every unwired path fails loudly.
contract WiringTest is Test {
    JobVault internal vault;
    FloatPool internal pool;

    address internal constant ADMIN    = address(0xA0);
    address internal constant STRANGER = address(0xBAD);
    address internal constant USDC     = address(0x05DC);

    event Wired(address indexed target);

    function setUp() public {
        vm.startPrank(ADMIN);
        vault = new JobVault(IERC20(USDC));
        pool = new FloatPool(IERC20(USDC));
        vm.stopPrank();
    }

    // ─────────────────────────────────────────────────────────────────────
    // JobVault.wireFloatPool
    // ─────────────────────────────────────────────────────────────────────

    function test_wireFloatPool_bindsAndEmits() public {
        assertEq(address(vault.floatPool()), address(0), "starts unwired");

        vm.expectEmit(true, true, true, true);
        emit Wired(address(pool));
        vm.prank(ADMIN);
        vault.wireFloatPool(address(pool));

        assertEq(address(vault.floatPool()), address(pool), "pool bound");
    }

    function test_wireFloatPool_revertsForNonAdmin() public {
        vm.expectRevert(JobVault.NotAuthorized.selector);
        vm.prank(STRANGER);
        vault.wireFloatPool(address(pool));
    }

    /// One-shot. A second wire would let an admin redirect the waterfall's repayment leg
    /// to a pool they control, after customers had already funded jobs.
    function test_wireFloatPool_revertsOnRewire() public {
        vm.startPrank(ADMIN);
        vault.wireFloatPool(address(pool));

        FloatPool impostor = new FloatPool(IERC20(USDC));
        vm.expectRevert(JobVault.AlreadyWired.selector);
        vault.wireFloatPool(address(impostor));
        vm.stopPrank();

        assertEq(address(vault.floatPool()), address(pool), "original binding survives");
    }

    function test_wireFloatPool_revertsOnZeroAddress() public {
        vm.expectRevert(JobVault.ZeroAddress.selector);
        vm.prank(ADMIN);
        vault.wireFloatPool(address(0));
    }

    // ─────────────────────────────────────────────────────────────────────
    // FloatPool.wireJobVault
    // ─────────────────────────────────────────────────────────────────────

    function test_wireJobVault_bindsAndEmits() public {
        assertEq(address(pool.jobVault()), address(0), "starts unwired");

        vm.expectEmit(true, true, true, true);
        emit Wired(address(vault));
        vm.prank(ADMIN);
        pool.wireJobVault(address(vault));

        assertEq(address(pool.jobVault()), address(vault), "vault bound");
    }

    function test_wireJobVault_revertsForNonAdmin() public {
        vm.expectRevert(FloatPool.NotAuthorized.selector);
        vm.prank(STRANGER);
        pool.wireJobVault(address(vault));
    }

    function test_wireJobVault_revertsOnRewire() public {
        vm.startPrank(ADMIN);
        pool.wireJobVault(address(vault));

        JobVault impostor = new JobVault(IERC20(USDC));
        vm.expectRevert(FloatPool.AlreadyWired.selector);
        pool.wireJobVault(address(impostor));
        vm.stopPrank();

        assertEq(address(pool.jobVault()), address(vault), "original binding survives");
    }

    function test_wireJobVault_revertsOnZeroAddress() public {
        vm.expectRevert(FloatPool.ZeroAddress.selector);
        vm.prank(ADMIN);
        pool.wireJobVault(address(0));
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-FP-010 — an unwired pool accepts nothing
    // ─────────────────────────────────────────────────────────────────────

    /// A half-deployed system must fail loudly. Before wiring there is no registered
    /// JobVault, so there is no caller that could legitimately repay or write off.
    function test_unwiredPool_rejectsRepayAndWriteOff() public {
        bytes32 job = keccak256("job_104");

        vm.expectRevert(FloatPool.NotWired.selector);
        pool.repayAdvance(job, 1);

        vm.expectRevert(FloatPool.NotWired.selector);
        pool.writeOff(job);
    }

    /// Once wired, everyone except the registered vault is still rejected (SC-FP-010).
    function test_wiredPool_rejectsEveryCallerButTheVault() public {
        bytes32 job = keccak256("job_104");

        vm.prank(ADMIN);
        pool.wireJobVault(address(vault));

        for (uint256 i = 0; i < 3; i++) {
            address caller = [ADMIN, STRANGER, address(this)][i];
            vm.expectRevert(FloatPool.NotJobVault.selector);
            vm.prank(caller);
            pool.repayAdvance(job, 1);

            vm.expectRevert(FloatPool.NotJobVault.selector);
            vm.prank(caller);
            pool.writeOff(job);
        }
    }

    /// The deploy script's end state (script/Deploy.s.sol): both directions bound.
    function test_deployWiring_isBidirectional() public {
        vm.startPrank(ADMIN);
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));
        vm.stopPrank();

        assertEq(address(vault.floatPool()), address(pool), "vault -> pool");
        assertEq(address(pool.jobVault()), address(vault), "pool -> vault");
    }
}
