// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

// Stateful invariant fuzzing of JobVault + FloatPool together (internal review, Nov 2026).
//
// A handler drives LPs, two allowlisted operators, one operator the pool never allowed, and
// customers through deposits, withdrawals and every job ending: accept, finalize after the
// review period, dispute and resolve, voluntary and timeout refunds. Every call is wrapped in
// try/catch, so reverts are expected; the invariants are what must hold afterwards.

import {Test, console2} from "forge-std/Test.sol";
import {IERC20} from "@openzeppelin/contracts/token/ERC20/IERC20.sol";
import {JobVault} from "../src/JobVault.sol";
import {FloatPool} from "../src/FloatPool.sol";
import {MockUSDC} from "./mocks/MockUSDC.sol";

contract Handler is Test {
    JobVault public vault;
    FloatPool public pool;
    MockUSDC public usdc;

    address public constant ADMIN = address(0xA0);
    address[3] public lps = [address(0x1D1), address(0x1D2), address(0x1D3)];
    // ops[0], ops[1] allowed; ops[2] never allowed
    address[3] public ops = [address(0x0901), address(0x0902), address(0x0903)];
    address[3] public custs = [address(0xC01), address(0xC02), address(0xC03)];

    bytes32[] public jobIds;
    uint256 public nonce;
    uint256 public ghostMinted;
    uint256 public ghostUnallowedAdvances;
    uint256 public nAdv; uint256 public nAcc; uint256 public nRef; uint256 public nWd; uint256 public nDep; uint256 public nFund;

    constructor(JobVault v, FloatPool p, MockUSDC u) {
        vault = v; pool = p; usdc = u;
        for (uint256 i; i < 3; i++) {
            vm.prank(lps[i]); usdc.approve(address(pool), type(uint256).max);
            vm.prank(custs[i]); usdc.approve(address(vault), type(uint256).max);
        }
    }

    function _mint(address to, uint256 amt) internal { usdc.mint(to, amt); ghostMinted += amt; }

    function deposit(uint256 who, uint256 amt) external {
        address lp = lps[who % 3];
        amt = bound(amt, 1, 2000e6);
        _mint(lp, amt);
        vm.prank(lp);
        try pool.deposit(amt, lp) { nDep++; } catch {}
    }

    function withdraw(uint256 who, uint256 amt) external {
        address lp = lps[who % 3];
        amt = bound(amt, 1, 200e6);
        vm.prank(lp);
        try pool.withdraw(amt, lp, lp) { nWd++; } catch {}
    }

    function createAndFund(uint256 o, uint256 c, uint256 pay, uint256 dl) external {
        address op = ops[o % 3];
        address cu = custs[c % 3];
        pay = bound(pay, 1, 60e6);
        bytes32 id = keccak256(abi.encode("job", nonce++));
        uint64 deadline = uint64(block.timestamp + bound(dl, 0, 10 days));
        vm.prank(op);
        try vault.createJob(id, cu, op, pay, pay, keccak256("t"), deadline) {
            jobIds.push(id);
            _mint(cu, pay);
            vm.prank(cu);
            try vault.fund(id) { nFund++; } catch {}
        } catch {}
    }

    function _job(uint256 i) internal view returns (bytes32 id, bool ok) {
        if (jobIds.length == 0) return (bytes32(0), false);
        return (jobIds[i % jobIds.length], true);
    }

    function advance(uint256 i) external {
        (bytes32 id, bool ok) = _job(i); if (!ok) return;
        (address op,,) = vault.jobEconomics(id);
        vm.prank(op);
        try pool.requestAdvance(id) {
            nAdv++; if (op == ops[2]) ghostUnallowedAdvances++;
        } catch {}
    }

    function startAndDeliver(uint256 i, bool deliver) external {
        (bytes32 id, bool ok) = _job(i); if (!ok) return;
        (address op,,) = vault.jobEconomics(id);
        vm.startPrank(op);
        try vault.startWork(id) {} catch {}
        if (deliver) { try vault.submitDelivery(id, keccak256("d")) {} catch {} }
        vm.stopPrank();
    }

    function accept(uint256 i) external {
        (bytes32 id, bool ok) = _job(i); if (!ok) return;
        (address cu,,,,,,,,) = vault.jobs(id);
        vm.prank(cu);
        try vault.acceptDelivery(id) { nAcc++; } catch {}
    }

    function refund(uint256 i, uint256 by) external {
        (bytes32 id, bool ok) = _job(i); if (!ok) return;
        (address cu, address op,,,,,,,) = vault.jobs(id);
        address caller = by % 3 == 0 ? cu : (by % 3 == 1 ? op : ADMIN);
        vm.prank(caller);
        try vault.refund(id) { nRef++; } catch {}
    }

    // Biased path: create, fund, advance, start, deliver; ends in accept or refund.
    function fullCycle(uint256 o, uint256 pay, bool settle, uint256 by) external {
        address op = ops[o % 2];
        address cu = custs[o % 3];
        pay = bound(pay, 1e6, 60e6);
        bytes32 id = keccak256(abi.encode("job", nonce++));
        vm.prank(op);
        try vault.createJob(id, cu, op, pay, pay, keccak256("t"), uint64(block.timestamp + 1 days)) { jobIds.push(id); } catch { return; }
        _mint(cu, pay);
        vm.prank(cu); try vault.fund(id) { nFund++; } catch { return; }
        vm.prank(op); try pool.requestAdvance(id) { nAdv++; } catch {}
        vm.startPrank(op);
        vault.startWork(id);
        vault.submitDelivery(id, keccak256("d"));
        vm.stopPrank();
        if (settle) {
            if (by % 2 == 0) { vm.prank(cu); vault.acceptDelivery(id); }
            else { vm.warp(block.timestamp + vault.REVIEW_PERIOD()); vm.prank(op); vault.finalizeDelivery(id); }
            nAcc++;
        } else {
            if (by % 2 == 0) { vm.prank(op); vault.refund(id); }
            else { vm.prank(cu); vault.dispute(id); vm.prank(ADMIN); vault.resolveDispute(id, true); }
            nRef++;
        }
    }

    function finalize(uint256 i) external {
        (bytes32 id, bool ok) = _job(i); if (!ok) return;
        (address op,,) = vault.jobEconomics(id);
        vm.prank(op);
        try vault.finalizeDelivery(id) { nAcc++; } catch {}
    }

    function disputeAndResolve(uint256 i, bool refundCustomer) external {
        (bytes32 id, bool ok) = _job(i); if (!ok) return;
        (address cu,,,,,,,,) = vault.jobs(id);
        vm.prank(cu);
        try vault.dispute(id) {} catch {}
        vm.prank(ADMIN);
        try vault.resolveDispute(id, refundCustomer) {
            if (refundCustomer) nRef++;
            else nAcc++;
        } catch {}
    }

    function warp(uint256 s) external { vm.warp(block.timestamp + bound(s, 1, 3 days)); }

    function jobCount() external view returns (uint256) { return jobIds.length; }
}

/// forge-config: default.invariant.runs = 64
/// forge-config: default.invariant.depth = 100
contract InvariantsTest is Test {
    JobVault vault;
    FloatPool pool;
    MockUSDC usdc;
    Handler h;

    uint256 constant MAX_EXPOSURE = 3000e6;
    uint256 constant MAX_ADVANCE = 50e6;

    function setUp() public {
        usdc = new MockUSDC();
        vm.startPrank(address(0xA0));
        vault = new JobVault(IERC20(address(usdc)));
        pool = new FloatPool(IERC20(address(usdc)));
        vault.wireFloatPool(address(pool));
        pool.wireJobVault(address(vault));
        vault.setMaxJobPayment(60e6);
        pool.setOperatorAllowed(address(0x0901), true);
        pool.setOperatorAllowed(address(0x0902), true);
        pool.setCaps(MAX_EXPOSURE, MAX_ADVANCE);
        pool.setDepositorAllowed(address(0x1D1), true);
        pool.setDepositorAllowed(address(0x1D2), true);
        pool.setDepositorAllowed(address(0x1D3), true);
        vm.stopPrank();

        h = new Handler(vault, pool, usdc);
        targetContract(address(h));
    }

    function invariant_poolCashMatchesBooks() public view {
        assertEq(usdc.balanceOf(address(pool)), pool.totalAssets() - pool.totalOutstanding() + pool.reserve());
    }

    function invariant_sharesSum() public view {
        uint256 s;
        for (uint256 i; i < 3; i++) s += pool.sharesOf(h.lps(i));
        assertEq(s, pool.totalShares());
    }

    function invariant_outstandingMatchesOpenAdvances() public view {
        uint256 open;
        uint256 n = h.jobCount();
        for (uint256 i; i < n; i++) {
            (uint256 p,, bool o) = pool.openAdvanceOf(h.jobIds(i));
            if (o) open += p;
        }
        assertEq(open, pool.totalOutstanding());
        uint256 orgs;
        for (uint256 i; i < 3; i++) orgs += pool.orgOutstanding(h.ops(i));
        assertEq(orgs, pool.totalOutstanding());
    }

    function invariant_exposureCeiling() public view {
        assertLe(pool.totalOutstanding() + pool.totalWrittenOff(), MAX_EXPOSURE);
    }

    function invariant_vaultHoldsLiveEscrow() public view {
        uint256 live;
        uint256 n = h.jobCount();
        for (uint256 i; i < n; i++) {
            (,, uint256 pay,,,,,, JobVault.JobStatus st) = vault.jobs(h.jobIds(i));
            if (st == JobVault.JobStatus.Funded || st == JobVault.JobStatus.InProgress || st == JobVault.JobStatus.Delivered) {
                live += pay;
            }
        }
        assertEq(usdc.balanceOf(address(vault)), live);
    }

    function invariant_unallowedOperatorNeverBorrows() public view {
        assertEq(h.ghostUnallowedAdvances(), 0);
        assertEq(pool.orgOutstanding(h.ops(2)), 0);
    }

    function invariant_noValueCreated() public view {
        uint256 total = usdc.balanceOf(address(pool)) + usdc.balanceOf(address(vault));
        for (uint256 i; i < 3; i++) {
            total += usdc.balanceOf(h.lps(i)) + usdc.balanceOf(h.ops(i)) + usdc.balanceOf(h.custs(i));
        }
        assertEq(total, h.ghostMinted());
    }

    /// Proof that the run exercised the money paths rather than reverting through them.
    /// Visible with -vv.
    function afterInvariant() public view {
        console2.log("deposits withdrawals funded", h.nDep(), h.nWd(), h.nFund());
        console2.log("advances settled refunded", h.nAdv(), h.nAcc(), h.nRef());
        console2.log("totalWrittenOff reserve", pool.totalWrittenOff(), pool.reserve());
    }

    function invariant_solvencyOfIdle() public view {
        assertGe(pool.totalAssets(), pool.totalOutstanding());
    }
}
