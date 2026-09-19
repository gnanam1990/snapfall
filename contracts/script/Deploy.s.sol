// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Script, console2} from "forge-std/Script.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {AuditAnchor} from "../src/AuditAnchor.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";

/// @notice Deploys the three Snapfall contracts and wires JobVault <-> FloatPool (SPEC-04).
///
/// Deploy order: AuditAnchor -> JobVault(usdc) -> FloatPool(usdc) -> wire both directions.
/// The wiring is mandatory: an unwired JobVault cannot run the SC-JV-009 waterfall, and an
/// unwired FloatPool rejects repayAdvance/writeOff (SC-FP-010). Both setters are one-shot,
/// so a botched deploy means redeploying rather than repointing.
///
/// The capital controls are set in the SAME broadcast as the deployment, deliberately. They
/// fail closed, so stopping after the wiring would leave something inert rather than dangerous
/// — but it would also leave a live, unconfigured contract on a public chain, and the gap
/// between "deployed" and "capped" is exactly the state nobody should have to reason about.
/// Deploy, wire and cap, or do none of it.
///
/// Usage (either network):
///   export ARC_RPC=https://rpc.testnet.arc.network     # or the mainnet RPC
///   export ARC_USDC_ADDRESS=0x...        # the real USDC on that network
///   cast wallet import snapfall-deployer --interactive
///   export DEPLOYER_ADDRESS=0x...        # output of cast wallet address --account snapfall-deployer
///   export SNAPFALL_MAX_TOTAL_EXPOSURE=50000000   # 50.00 USDC, 6dp ERC-20 base units
///   export SNAPFALL_MAX_ADVANCE=10000000          # 10.00 USDC
///   export SNAPFALL_MAX_JOB_PAYMENT=25000000      # 25.00 USDC
///   forge script script/Deploy.s.sol --rpc-url "$ARC_RPC" \
///     --account snapfall-deployer --sender "$DEPLOYER_ADDRESS" --broadcast
///
/// For mainnet read docs/MAINNET.md first. The caps are the entire reason that deployment is
/// allowed to exist, and the numbers belong to the operator, not to this file.
///
/// Copy the logged addresses into docs/addresses.md and the README testnet notes.
contract Deploy is Script {
    function run() external {
        address usdc = vm.envAddress("ARC_USDC_ADDRESS");

        // No defaults. A ceiling nobody chose is not a ceiling, so these are required and the
        // script aborts before broadcasting anything if one is missing or nonsensical.
        uint256 maxTotalExposure = vm.envUint("SNAPFALL_MAX_TOTAL_EXPOSURE");
        uint256 maxAdvance = vm.envUint("SNAPFALL_MAX_ADVANCE");
        uint256 maxJobPayment = vm.envUint("SNAPFALL_MAX_JOB_PAYMENT");

        require(maxTotalExposure > 0, "SNAPFALL_MAX_TOTAL_EXPOSURE must be > 0");
        require(maxAdvance > 0, "SNAPFALL_MAX_ADVANCE must be > 0");
        require(maxJobPayment > 0, "SNAPFALL_MAX_JOB_PAYMENT must be > 0");
        require(maxAdvance <= maxTotalExposure, "per-advance cap cannot exceed the total ceiling");

        console2.log("usdc            ", usdc);
        console2.log("maxTotalExposure", maxTotalExposure);
        console2.log("maxAdvance      ", maxAdvance);
        console2.log("maxJobPayment   ", maxJobPayment);

        // The CLI supplies one encrypted keystore or hardware-wallet signer. Keeping
        // the raw key out of environment variables prevents process and shell-history leaks.
        vm.startBroadcast();

        AuditAnchor anchor = new AuditAnchor();
        JobVault vault = new JobVault(IERC20(usdc));
        FloatPool pool = new FloatPool(IERC20(usdc));

        // Wire both directions. Each is one-shot and admin-only; the deployer is admin.
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));

        // Capital controls. Until these land the pool lends nothing and the vault takes no
        // work. The deposit allowlist is already on, with the deployer as its only member.
        pool.setCaps(maxTotalExposure, maxAdvance);
        vault.setMaxJobPayment(maxJobPayment);

        vm.stopBroadcast();

        console2.log("AuditAnchor  ", address(anchor));
        console2.log("JobVault     ", address(vault));
        console2.log("FloatPool    ", address(pool));
        console2.log("");
        console2.log("wired: JobVault.floatPool  ->", address(vault.floatPool()));
        console2.log("wired: FloatPool.jobVault  ->", address(pool.jobVault()));
        console2.log("cap:   FloatPool.maxTotalExposure ->", pool.maxTotalExposure());
        console2.log("cap:   FloatPool.maxAdvance       ->", pool.maxAdvance());
        console2.log("cap:   JobVault.maxJobPayment     ->", vault.maxJobPayment());
        console2.log("allowlist enabled ->", pool.depositAllowlistEnabled());
    }
}
