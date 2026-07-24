// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {IIdleCapitalStrategy} from "../src/IIdleCapitalStrategy.sol";
import {MockUSYCStrategy} from "../src/strategies/MockUSYCStrategy.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

contract USYCSweepTest is Test {
    MockUSDC internal usdc;
    MockUSYCStrategy internal strategy;

    address internal constant POOL = address(0xF100);
    address internal constant OTHER_POOL = address(0xF200);
    address internal constant YIELD_DONOR = address(0xD0);

    function setUp() public {
        usdc = new MockUSDC();
        strategy = new MockUSYCStrategy(usdc);

        usdc.mint(POOL, 150_000_000);
        usdc.mint(OTHER_POOL, 50_000_000);
        usdc.mint(YIELD_DONOR, 10_000_000);

        vm.prank(POOL);
        usdc.approve(address(strategy), type(uint256).max);
        vm.prank(OTHER_POOL);
        usdc.approve(address(strategy), type(uint256).max);
        vm.prank(YIELD_DONOR);
        usdc.approve(address(strategy), type(uint256).max);
    }

    function test_mockImplementsTheIdleCapitalStrategySeam() public view {
        IIdleCapitalStrategy seam = IIdleCapitalStrategy(address(strategy));
        assertEq(seam.asset(), address(usdc));
        assertTrue(seam.isMock());
    }

    function test_depositMovesIdleUSDCAndReportsAssetBalance() public {
        vm.prank(POOL);
        uint256 shares = strategy.deposit(100_000_000);

        assertEq(shares, 100_000_000);
        assertEq(usdc.balanceOf(POOL), 50_000_000);
        assertEq(usdc.balanceOf(address(strategy)), 100_000_000);
        assertEq(strategy.balanceOf(POOL), 100_000_000);
    }

    function test_mockYieldRaisesPositionValueWithoutMintingShares() public {
        vm.prank(POOL);
        strategy.deposit(100_000_000);

        vm.prank(YIELD_DONOR);
        strategy.addMockYield(5_000_000);

        assertEq(strategy.sharesOf(POOL), 100_000_000);
        assertEq(strategy.balanceOf(POOL), 105_000_000);
    }

    function test_directDonationRaisesPositionValueInsteadOfBecomingTrapped() public {
        vm.prank(POOL);
        strategy.deposit(100_000_000);

        vm.prank(YIELD_DONOR);
        usdc.transfer(address(strategy), 5_000_000);

        assertEq(strategy.balanceOf(POOL), 105_000_000);
        assertEq(strategy.totalManagedAssets(), 105_000_000);
    }

    function test_shareMathDoesNotOverflowForLargeValidBalances() public {
        uint256 first = uint256(1) << 200;
        uint256 second = uint256(1) << 100;
        usdc.mint(POOL, first);
        usdc.mint(OTHER_POOL, second);

        vm.prank(POOL);
        strategy.deposit(first);
        vm.prank(OTHER_POOL);
        uint256 shares = strategy.deposit(second);

        assertEq(shares, second);
        assertEq(strategy.balanceOf(OTHER_POOL), second);
    }

    function test_redeemOnDemandReturnsPrincipalAndYield() public {
        vm.prank(POOL);
        uint256 shares = strategy.deposit(100_000_000);
        vm.prank(YIELD_DONOR);
        strategy.addMockYield(5_000_000);

        vm.prank(POOL);
        uint256 assets = strategy.redeem(shares, POOL);

        assertEq(assets, 105_000_000);
        assertEq(usdc.balanceOf(POOL), 155_000_000);
        assertEq(strategy.balanceOf(POOL), 0);
        assertEq(strategy.totalManagedAssets(), 0);
    }

    function test_positionsStayIsolatedAcrossPoolAccounts() public {
        vm.prank(POOL);
        strategy.deposit(100_000_000);
        vm.prank(OTHER_POOL);
        strategy.deposit(50_000_000);
        vm.prank(YIELD_DONOR);
        strategy.addMockYield(6_000_000);

        assertEq(strategy.balanceOf(POOL), 104_000_000);
        assertEq(strategy.balanceOf(OTHER_POOL), 52_000_000);
    }

    function test_redeemRejectsAnotherAccountsShares() public {
        vm.prank(POOL);
        strategy.deposit(100_000_000);

        vm.prank(OTHER_POOL);
        vm.expectRevert(MockUSYCStrategy.InsufficientShares.selector);
        strategy.redeem(1, OTHER_POOL);
    }

    function test_mockYieldCannotBeAddedWithoutAnActivePosition() public {
        vm.prank(YIELD_DONOR);
        vm.expectRevert(MockUSYCStrategy.NoActivePosition.selector);
        strategy.addMockYield(1_000_000);
    }
}
