// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {IIdleCapitalStrategy} from "../IIdleCapitalStrategy.sol";

/// @title Mock USYC idle-capital strategy
/// @notice Testnet fallback for FR-FLT-009 / SC-FP-007. It moves real ERC-20 assets,
///         represents positions as internal shares, and lets donated yield raise share value.
///         It never claims to be the permissioned Circle/Hashnote USYC product.
contract MockUSYCStrategy is IIdleCapitalStrategy, ReentrancyGuard {
    using SafeERC20 for IERC20;

    IERC20 public immutable usdc;
    uint256 public totalManagedAssets;
    uint256 public totalShares;
    mapping(address => uint256) public sharesOf;

    event Deposited(address indexed account, uint256 assets, uint256 shares);
    event Redeemed(address indexed account, address indexed receiver, uint256 shares, uint256 assets);
    event MockYieldAdded(address indexed donor, uint256 assets);

    error ZeroAddress();
    error ZeroAmount();
    error InsufficientShares();
    error NoActivePosition();

    constructor(IERC20 asset_) {
        if (address(asset_) == address(0)) revert ZeroAddress();
        usdc = asset_;
    }

    function asset() external view returns (address) {
        return address(usdc);
    }

    function isMock() external pure returns (bool) {
        return true;
    }

    function deposit(uint256 assets) external nonReentrant returns (uint256 shares) {
        if (assets == 0) revert ZeroAmount();
        shares = totalShares == 0 ? assets : (assets * totalShares) / totalManagedAssets;
        if (shares == 0) revert ZeroAmount();

        totalManagedAssets += assets;
        totalShares += shares;
        sharesOf[msg.sender] += shares;
        emit Deposited(msg.sender, assets, shares);

        usdc.safeTransferFrom(msg.sender, address(this), assets);
    }

    function redeem(uint256 shares, address receiver) external nonReentrant returns (uint256 assets) {
        if (shares == 0) revert ZeroAmount();
        if (receiver == address(0)) revert ZeroAddress();
        if (shares > sharesOf[msg.sender]) revert InsufficientShares();

        assets = (shares * totalManagedAssets) / totalShares;
        sharesOf[msg.sender] -= shares;
        totalShares -= shares;
        totalManagedAssets -= assets;
        emit Redeemed(msg.sender, receiver, shares, assets);

        usdc.safeTransfer(receiver, assets);
    }

    function balanceOf(address account) external view returns (uint256 assets) {
        if (totalShares == 0) return 0;
        return (sharesOf[account] * totalManagedAssets) / totalShares;
    }

    /// @notice Simulates external USYC yield by donating USDC without minting shares.
    function addMockYield(uint256 assets) external nonReentrant {
        if (assets == 0) revert ZeroAmount();
        if (totalShares == 0) revert NoActivePosition();
        totalManagedAssets += assets;
        emit MockYieldAdded(msg.sender, assets);
        usdc.safeTransferFrom(msg.sender, address(this), assets);
    }
}
