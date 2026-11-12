// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {AuditAnchor} from "../src/AuditAnchor.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";

/// Drift tripwire for the hand-curated event ABIs in abi/indexer/ (deployments/*.json point the
/// daemon's indexer config at them; see deployments/README.md).
///
/// They are a deliberate subset of each contract's events, so they cannot simply be copied from
/// out/. That made them easy to forget: the Nov 2026 operator allowlist events were missing until
/// a review caught it. This test pins, per file, exactly which events it carries and in what
/// order, and checks every entry's name, argument types and indexed flags against the compiled
/// contract. Adding an event to a contract the indexer should see means adding it to the file
/// and to the expected list below.
///
/// To regenerate an entry, copy the event object from out/<Contract>.sol/<Contract>.json
/// (`forge inspect <Contract> abi --json`), keeping only type, name, anonymous and inputs
/// (name, type, indexed), one event per line.
contract IndexerAbiTest is Test {
    function test_floatPoolIndexerAbi() public view {
        bytes32[] memory expected = new bytes32[](7);
        expected[0] = FloatPool.AdvanceIssued.selector;
        expected[1] = FloatPool.AdvanceRepaid.selector;
        expected[2] = FloatPool.AdvanceWrittenOff.selector;
        expected[3] = FloatPool.RateChanged.selector;
        expected[4] = FloatPool.OperatorAllowed.selector;
        expected[5] = FloatPool.OperatorGrantQueued.selector;
        expected[6] = FloatPool.OperatorGrantCancelled.selector;
        _check("FloatPool", expected);
    }

    function test_jobVaultIndexerAbi() public view {
        bytes32[] memory expected = new bytes32[](4);
        expected[0] = JobVault.JobFunded.selector;
        expected[1] = JobVault.ExpenseRecorded.selector;
        expected[2] = JobVault.DeliverySubmitted.selector;
        expected[3] = JobVault.JobSettled.selector;
        _check("JobVault", expected);
    }

    function test_auditAnchorIndexerAbi() public view {
        bytes32[] memory expected = new bytes32[](1);
        expected[0] = AuditAnchor.JobAnchored.selector;
        _check("AuditAnchor", expected);
    }

    function _check(string memory name, bytes32[] memory expected) internal view {
        string memory curated = vm.readFile(string.concat("abi/indexer/", name, ".json"));
        string memory artifact = vm.readFile(string.concat("out/", name, ".sol/", name, ".json"));

        uint256 n;
        while (vm.keyExistsJson(curated, string.concat("$[", vm.toString(n), "]"))) n++;
        assertEq(n, expected.length, string.concat(name, ": event count"));

        for (uint256 i = 0; i < n; i++) {
            _checkEntry(curated, artifact, string.concat("$[", vm.toString(i), "]"), expected[i]);
        }
    }

    function _checkEntry(string memory curated, string memory artifact, string memory at, bytes32 expected)
        internal
        view
    {
        assertEq(vm.parseJsonString(curated, string.concat(at, ".type")), "event", "entry is an event");
        assertFalse(vm.parseJsonBool(curated, string.concat(at, ".anonymous")), "not anonymous");
        string memory eventName = vm.parseJsonString(curated, string.concat(at, ".name"));

        // Name + types must hash to the contract's real event topic, in the expected slot, and
        // the indexed flags (topics vs data) must match the compiler's exactly.
        string memory compiled = string.concat("$.abi[?(@.type == 'event' && @.name == '", eventName, "')]");
        string memory sig = string.concat(eventName, "(");
        uint256 k;
        for (; vm.keyExistsJson(curated, _input(at, k)); k++) {
            string memory input = _input(at, k);
            sig = string.concat(sig, k == 0 ? "" : ",", vm.parseJsonString(curated, string.concat(input, ".type")));
            assertEq(
                vm.parseJsonBool(curated, string.concat(input, ".indexed")),
                vm.parseJsonBool(artifact, string.concat(_input(compiled, k), ".indexed")),
                string.concat("indexed flag of ", eventName)
            );
        }
        sig = string.concat(sig, ")");
        assertEq(keccak256(bytes(sig)), expected, string.concat("topic of ", sig));
        assertFalse(vm.keyExistsJson(artifact, _input(compiled, k)), string.concat("arity of ", eventName));
    }

    function _input(string memory at, uint256 k) internal view returns (string memory) {
        return string.concat(at, ".inputs[", vm.toString(k), "]");
    }
}
