// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {SafeERC20} from "@openzeppelin/contracts/token/ERC20/utils/SafeERC20.sol";
import {Math} from "@openzeppelin/contracts/utils/math/Math.sol";
import {ReentrancyGuard} from "@openzeppelin/contracts/utils/ReentrancyGuard.sol";
import {IIdleCapitalStrategy} from "../IIdleCapitalStrategy.sol";

/// @title Mock USYC idle-capital strategy
/// @notice Testnet fallback for FR-FLT-009 / SC-FP-007. It moves real ERC-20 assets,
///         represents positions as internal shares, and lets donated yield raise share value.
///         It never claims to be the permissioned Circle/Hashnote USYC product.
contract MockUSYCStrategy is IIdleCapitalStrategy, ReentrancyGuard {
    using SafeERC20 for IERC20;

    IERC20 public immutable usdc;
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

    function asset() external view override returns (address) {
        return address(usdc);
    }

    function isMock() external pure override returns (bool) {
        return true;
    }

    /// @notice The underlying token balance is the accounting truth. This also makes a
    ///         direct donation visible as simulated yield instead of trapping it outside
    ///         position values. Fee-on-transfer assets mint shares only for what arrived.
    function deposit(uint256 assets) external override nonReentrant returns (uint256 shares) {
        if (assets == 0) revert ZeroAmount();

        uint256 managedBefore = totalManagedAssets();
        usdc.safeTransferFrom(msg.sender, address(this), assets);
        uint256 received = totalManagedAssets() - managedBefore;
        shares = totalShares == 0 ? received : Math.mulDiv(received, totalShares, managedBefore);
        if (shares == 0) revert ZeroAmount();

        totalShares += shares;
        sharesOf[msg.sender] += shares;
        emit Deposited(msg.sender, received, shares);
    }

    function redeem(uint256 shares, address receiver) external override nonReentrant returns (uint256 assets) {
        if (shares == 0) revert ZeroAmount();
        if (receiver == address(0)) revert ZeroAddress();
        if (shares > sharesOf[msg.sender]) revert InsufficientShares();

        assets = Math.mulDiv(shares, totalManagedAssets(), totalShares);
        sharesOf[msg.sender] -= shares;
        totalShares -= shares;
        emit Redeemed(msg.sender, receiver, shares, assets);

        usdc.safeTransfer(receiver, assets);
    }

    function balanceOf(address account) external view override returns (uint256 assets) {
        if (totalShares == 0) return 0;
        return Math.mulDiv(sharesOf[account], totalManagedAssets(), totalShares);
    }

    /// @notice Assets currently controlled by the mock strategy.
    function totalManagedAssets() public view returns (uint256) {
        return usdc.balanceOf(address(this));
    }

    /// @notice Simulates external USYC yield by donating USDC without minting shares.
    function addMockYield(uint256 assets) external nonReentrant {
        if (assets == 0) revert ZeroAmount();
        if (totalShares == 0) revert NoActivePosition();
        uint256 managedBefore = totalManagedAssets();
        usdc.safeTransferFrom(msg.sender, address(this), assets);
        uint256 received = totalManagedAssets() - managedBefore;
        if (received == 0) revert ZeroAmount();
        emit MockYieldAdded(msg.sender, received);
    }
}
