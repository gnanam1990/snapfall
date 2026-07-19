// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

/// Minimal ERC20 surface used across Snapfall contracts.
/// Replace usage with OpenZeppelin SafeERC20 after `forge install OpenZeppelin/openzeppelin-contracts`.
interface IERC20 {
    function transfer(address to, uint256 amount) external returns (bool);
    function transferFrom(address from, address to, uint256 amount) external returns (bool);
    function balanceOf(address account) external view returns (uint256);
    function approve(address spender, uint256 amount) external returns (bool);
}

interface IFloatPool {
    function repayAdvance(bytes32 jobId, uint256 amount) external;
    function writeOff(bytes32 jobId) external;
    function openAdvanceOf(bytes32 jobId) external view returns (uint256 principal, uint256 fee, bool open);
}

interface IJobVaultView {
    enum JobStatus { Created, Funded, InProgress, Delivered, Accepted, Refunded, Cancelled }
    function jobStatus(bytes32 jobId) external view returns (JobStatus);
    function jobEconomics(bytes32 jobId) external view returns (address operator, uint256 customerPayment, uint256 maxOperatingBudget);
}
