// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {FloatPool} from "../../src/FloatPool.sol";

/// @dev Test-only harness exposing a setter for delivery history.
///
/// In production `acceptedJobs` / `writtenOffJobs` are written only by repayAdvance and
/// writeOff, so exercising the rate curve — or an org that already has history — needs a
/// way to seed them. The setter lives HERE and never on FloatPool: the ABI freezes Fri
/// Jul 24 and test scaffolding must not leak into it.
///
/// Preferred over `vm.store`, which hard-codes storage slot numbers and silently writes the
/// wrong mapping the moment a state variable is added or a base contract changes.
contract FloatPoolHarness is FloatPool {
    constructor(IERC20 _usdc) FloatPool(_usdc) {}

    function setHistory(address org, uint32 accepted, uint32 writtenOff) external {
        acceptedJobs[org] = accepted;
        writtenOffJobs[org] = writtenOff;
    }
}
