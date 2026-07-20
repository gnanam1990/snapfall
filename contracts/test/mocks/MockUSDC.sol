// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";

/// @dev 6-decimal USDC stand-in.
///
/// Arc's native USDC exposes 18dp for gas/native transfers and 6dp for ERC-20 operations on
/// the same underlying balance (docs.arc.io evm-differences). Snapfall's contracts only ever
/// touch the ERC-20 surface, so 6dp is what belongs here — `25.00 USDC == 25_000_000`.
///
/// Every movement is appended to `transferLog`, which is how the waterfall test proves the
/// pool was paid BEFORE the operator (SC-JV-009) rather than merely in the same transaction.
contract MockUSDC is IERC20 {
    string public constant name = "Mock USD Coin";
    string public constant symbol = "USDC";
    uint8 public constant decimals = 6;

    uint256 public totalSupply;
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    struct Movement {
        address from;
        address to;
        uint256 amount;
    }

    Movement[] public transferLog;

    function transferCount() external view returns (uint256) {
        return transferLog.length;
    }

    /// Index of the first transfer into `to`, or type(uint256).max if there was none.
    function firstTransferTo(address to) external view returns (uint256) {
        for (uint256 i = 0; i < transferLog.length; i++) {
            if (transferLog[i].to == to) return i;
        }
        return type(uint256).max;
    }

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
        totalSupply += amount;
        emit Transfer(address(0), to, amount);
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        transferLog.push(Movement(msg.sender, to, amount));
        emit Transfer(msg.sender, to, amount);
        return true;
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        emit Approval(msg.sender, spender, amount);
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        uint256 allowed = allowance[from][msg.sender];
        if (allowed != type(uint256).max) allowance[from][msg.sender] = allowed - amount;
        balanceOf[from] -= amount;
        balanceOf[to] += amount;
        transferLog.push(Movement(from, to, amount));
        emit Transfer(from, to, amount);
        return true;
    }
}
