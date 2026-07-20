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
/// Usage (Arc testnet):
///   export ARC_TESTNET_RPC=https://rpc.testnet.arc.network
///   export ARC_USDC_ADDRESS=0x...        # the real USDC on Arc testnet
///   export TREASURY_PRIVATE_KEY=0x...    # TESTNET ONLY
///   forge script script/Deploy.s.sol --rpc-url "$ARC_TESTNET_RPC" --broadcast
///
/// Copy the logged addresses into docs/addresses.md and the README testnet notes.
contract Deploy is Script {
    function run() external {
        address usdc = vm.envAddress("ARC_USDC_ADDRESS");
        uint256 pk = vm.envUint("TREASURY_PRIVATE_KEY");
        address deployer = vm.addr(pk);

        console2.log("deployer     ", deployer);
        console2.log("usdc         ", usdc);

        vm.startBroadcast(pk);

        AuditAnchor anchor = new AuditAnchor();
        JobVault vault = new JobVault(IERC20(usdc));
        FloatPool pool = new FloatPool(IERC20(usdc));

        // Wire both directions. Each is one-shot and admin-only; the deployer is admin.
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));

        vm.stopBroadcast();

        console2.log("AuditAnchor  ", address(anchor));
        console2.log("JobVault     ", address(vault));
        console2.log("FloatPool    ", address(pool));
        console2.log("");
        console2.log("wired: JobVault.floatPool ->", address(vault.floatPool()));
        console2.log("wired: FloatPool.jobVault ->", address(pool.jobVault()));
    }
}
