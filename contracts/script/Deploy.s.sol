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
///   export SNAPFALL_OPERATOR=0x...               # the operator treasury allowed to borrow
///   export SNAPFALL_ADMIN=0x...                  # optional: admin handoff, accepted later
///   forge script script/Deploy.s.sol --rpc-url "$ARC_RPC" \
///     --account snapfall-deployer --sender "$DEPLOYER_ADDRESS" --broadcast
///
/// For mainnet read docs/MAINNET.md first. The caps are the entire reason that deployment is
/// allowed to exist, and the numbers belong to the operator, not to this file.
///
/// Copy the logged addresses into docs/addresses.md and the README testnet notes.
contract Deploy is Script {
    /// Everything the deployment depends on. run() fills it from the environment; the logic lives
    /// in deploy() so DeployScript.t.sol can exercise exactly what ships, without env or keys.
    struct Config {
        address usdc;
        uint256 maxTotalExposure;
        uint256 maxAdvance;
        uint256 maxJobPayment;
        address operator;
        address newAdmin; // address(0) = no handoff
    }

    function run() external returns (AuditAnchor, JobVault, FloatPool) {
        // No defaults. A ceiling nobody chose is not a ceiling, so these are required and the
        // script aborts before broadcasting anything if one is missing or nonsensical.
        Config memory cfg = Config({
            usdc: vm.envAddress("ARC_USDC_ADDRESS"),
            maxTotalExposure: vm.envUint("SNAPFALL_MAX_TOTAL_EXPOSURE"),
            maxAdvance: vm.envUint("SNAPFALL_MAX_ADVANCE"),
            maxJobPayment: vm.envUint("SNAPFALL_MAX_JOB_PAYMENT"),
            // The one address allowed to draw advances: the daemon's operator treasury, the same
            // address it signs requestAdvance from (SNAPFALL_TREASURY_ADDRESS in the daemon's
            // env). A public address, not a key. Required for the same reason the caps are:
            // borrowing is an admin grant that fails closed, and once the caps are set a grant
            // waits OPERATOR_GRANT_DELAY, so leaving it out here would mean a deployment that
            // cannot lend for two days. Anyone else who names themselves operator in JobVault
            // cannot borrow.
            operator: vm.envAddress("SNAPFALL_OPERATOR"),
            // Optional: start handing admin off the deployer key in the same run. The new admin
            // calls acceptAdmin() on both contracts from its own signer (a multisig or hardware
            // wallet) once ADMIN_TRANSFER_DELAY has passed, so a wrong address here strands
            // nothing: until then the deployer stays admin and can cancel with
            // transferAdmin(deployer).
            newAdmin: vm.envOr("SNAPFALL_ADMIN", address(0))
        });
        return deploy(cfg);
    }

    function deploy(Config memory cfg) public returns (AuditAnchor anchor, JobVault vault, FloatPool pool) {
        require(cfg.maxTotalExposure > 0, "SNAPFALL_MAX_TOTAL_EXPOSURE must be > 0");
        require(cfg.maxAdvance > 0, "SNAPFALL_MAX_ADVANCE must be > 0");
        require(cfg.maxJobPayment > 0, "SNAPFALL_MAX_JOB_PAYMENT must be > 0");
        require(cfg.maxAdvance <= cfg.maxTotalExposure, "per-advance cap cannot exceed the total ceiling");
        require(cfg.operator != address(0), "SNAPFALL_OPERATOR must be set");

        address usdc = cfg.usdc;
        uint256 maxTotalExposure = cfg.maxTotalExposure;
        uint256 maxAdvance = cfg.maxAdvance;
        uint256 maxJobPayment = cfg.maxJobPayment;
        address operator = cfg.operator;
        address newAdmin = cfg.newAdmin;

        console2.log("usdc            ", usdc);
        console2.log("maxTotalExposure", maxTotalExposure);
        console2.log("maxAdvance      ", maxAdvance);
        console2.log("maxJobPayment   ", maxJobPayment);
        console2.log("operator        ", operator);

        // The CLI supplies one encrypted keystore or hardware-wallet signer. Keeping
        // the raw key out of environment variables prevents process and shell-history leaks.
        vm.startBroadcast();

        anchor = new AuditAnchor();
        vault = new JobVault(IERC20(usdc));
        pool = new FloatPool(IERC20(usdc));

        // Wire both directions. Each is one-shot and admin-only; the deployer is admin.
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));

        // Capital controls. Until these land the pool lends nothing and the vault takes no
        // work. The deposit allowlist is already on, with the deployer as its only member.
        // ORDER MATTERS: the operator grant goes before the first setCaps, which is the only
        // point at which a grant applies immediately. After it, a grant is queued.
        // test/DeployScript.t.sol fails if these two lines are swapped.
        pool.setOperatorAllowed(operator, true);
        pool.setCaps(maxTotalExposure, maxAdvance);
        vault.setMaxJobPayment(maxJobPayment);

        if (newAdmin != address(0)) {
            pool.transferAdmin(newAdmin);
            vault.transferAdmin(newAdmin);
        }

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
        console2.log("operator allowed  ->", pool.allowedOperator(operator));
        console2.log("admin: FloatPool  ->", pool.admin());
        console2.log("admin: JobVault   ->", vault.admin());
        // Read the contracts, not the env: SNAPFALL_ADMIN equal to the deployer cancels instead
        // of starting a transfer, and telling the operator to call acceptAdmin() then would be wrong.
        address pending = pool.pendingAdmin();
        if (pending != address(0)) {
            console2.log("pending admin     ->", pending);
            console2.log("  acceptAdmin() on FloatPool and JobVault from that signer after", pool.adminTransferAcceptableAt());
        }
    }
}
