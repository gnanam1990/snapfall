// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

// TODO(A): forge-std after install: import {Test} from "forge-std/Test.sol";
// Test law (PRD §7.5) — required cases, write these FIRST:
//  [ ] advanceRate: base 50%, +5%/accepted, −15%/writeOff, clamps at 30%/85%
//  [ ] requestAdvance: reverts unless vault says Funded; duplicate reverts; amount = min(budget, rate×payment)
//  [ ] advance transfers ONLY to registered treasury
//  [ ] waterfall ordering: pool repaid (principal+fee) BEFORE operator transfer, same tx
//  [ ] write-off waterfall ordering: bond → reserve → LP shares, events per stage
//  [ ] exposure cap (10% TVL) + utilization cap (80%) enforced
//  [ ] reentrancy assumptions; fuzz: share/amount accounting invariants
