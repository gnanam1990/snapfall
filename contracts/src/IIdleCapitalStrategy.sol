// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

/// @title Idle-capital strategy seam for FloatPool
/// @notice A real adapter can wrap the permissioned USYC Teller; the shipped mock implements
///         the same deposit, marked-balance, and redeem-on-demand behavior.
interface IIdleCapitalStrategy {
    /// @notice ERC-20 asset accepted and returned by the strategy.
    function asset() external view returns (address);

    /// @notice Deposit assets from the caller and return strategy shares.
    function deposit(uint256 assets) external returns (uint256 shares);

    /// @notice Burn caller shares and return assets to receiver.
    function redeem(uint256 shares, address receiver) external returns (uint256 assets);

    /// @notice Current asset-denominated value of one account's strategy position.
    function balanceOf(address account) external view returns (uint256 assets);

    /// @notice Explicit disclosure used by the dashboard and demo caption.
    function isMock() external view returns (bool);
}
