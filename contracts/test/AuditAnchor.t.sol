// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Test} from "forge-std/Test.sol";
import {AuditAnchor} from "../src/AuditAnchor.sol";

/// AT-08 — audit completeness: the receipt of a completed job carries every piece of
/// evidence, and the root an auditor recomputes from that evidence matches what was
/// anchored on chain. This is the audit spine (PRD §7.3, SC-AA-001..004); before this
/// file it had zero behavioural coverage.
///
/// AuditAnchor stores roots verbatim — it does no on-chain root construction. So the
/// meaningful round trip is: build the roots off chain the way the daemon does (an
/// ordered fold over the job's event hashes), anchor them, read the slot back, and assert
/// the stored roots equal the recomputed ones. The negative direction matters just as
/// much: tamper with one leaf, recompute, and the root must no longer match — otherwise
/// "root matches anchor" would prove nothing.
contract AuditAnchorTest is Test {
    AuditAnchor internal anchor;

    address internal constant OPERATOR = address(0xA0);
    address internal constant STRANGER = address(0xBAD);

    event JobAnchored(
        bytes32 indexed jobId,
        bytes32 eventRoot,
        bytes32 paymentReceiptRoot,
        bytes32 deliveryHash,
        uint64 completedAt
    );

    function setUp() public {
        vm.prank(OPERATOR);
        anchor = new AuditAnchor();
    }

    // ─────────────────────────────────────────────────────────────────────
    // helpers — model the daemon's off-chain root construction
    // ─────────────────────────────────────────────────────────────────────

    /// Ordered fold over event hashes. Order is part of the commitment: reordering the
    /// leaves changes the root, which is what an event log's append-only ordering buys.
    function _fold(bytes32[] memory leaves) internal pure returns (bytes32 root) {
        for (uint256 i = 0; i < leaves.length; i++) {
            root = keccak256(abi.encodePacked(root, leaves[i]));
        }
    }

    /// The evidence set for job_004's happy path, as the daemon would hash it: the ordered
    /// chain events, then a separate fold over just the money movements for the payment
    /// receipt root, then the delivery artifact hash.
    function _job004Roots()
        internal
        pure
        returns (bytes32 eventRoot, bytes32 paymentReceiptRoot, bytes32 deliveryHash)
    {
        bytes32[] memory events = new bytes32[](6);
        events[0] = keccak256("JobFunded(job_004,25.00)");
        events[1] = keccak256("AdvanceIssued(job_004,12.50,0.25,5000)");
        events[2] = keccak256("ExpenseRecorded(job_004,0.04)");
        events[3] = keccak256("DeliverySubmitted(job_004,0xdeliv)");
        events[4] = keccak256("AdvanceRepaid(job_004,12.75)");
        events[5] = keccak256("JobSettled(job_004,pool=12.75,operator=12.25)");
        eventRoot = _fold(events);

        bytes32[] memory receipts = new bytes32[](3);
        receipts[0] = keccak256("pay:advance->treasury:12.50");
        receipts[1] = keccak256("pay:repay->pool:12.75");
        receipts[2] = keccak256("pay:settle->operator:12.25");
        paymentReceiptRoot = _fold(receipts);

        deliveryHash = keccak256("job_004 delivery artifact bytes");
    }

    // ─────────────────────────────────────────────────────────────────────
    // AT-08 round trip — the recomputed root matches the anchored one
    // ─────────────────────────────────────────────────────────────────────

    function test_anchorJob_roundTripRootMatchesRecomputed() public {
        bytes32 jobId = keccak256("job_004");
        (bytes32 eventRoot, bytes32 receiptRoot, bytes32 deliveryHash) = _job004Roots();
        uint64 completedAt = 53_268_445;

        vm.expectEmit(true, true, true, true);
        emit JobAnchored(jobId, eventRoot, receiptRoot, deliveryHash, completedAt);

        vm.prank(OPERATOR);
        anchor.anchorJob(jobId, eventRoot, receiptRoot, deliveryHash, completedAt);

        (
            bytes32 storedEvent,
            bytes32 storedReceipt,
            bytes32 storedDelivery,
            uint64 storedAt,
            bool finalized
        ) = anchor.anchors(jobId);

        // An auditor recomputes the roots from the same evidence and they match the slot.
        (bytes32 reEvent, bytes32 reReceipt, bytes32 reDelivery) = _job004Roots();
        assertEq(storedEvent, reEvent, "event root matches recomputed");
        assertEq(storedReceipt, reReceipt, "payment receipt root matches recomputed");
        assertEq(storedDelivery, reDelivery, "delivery hash matches recomputed");
        assertEq(storedAt, completedAt, "completedAt stored verbatim");
        assertTrue(finalized, "anchoring finalizes the slot");
    }

    /// The negative direction: if the evidence is tampered with, the recomputed root must
    /// no longer match what was anchored. Without this, "root matches" is vacuous.
    function test_anchorJob_tamperedEvidenceFailsRootMatch() public {
        bytes32 jobId = keccak256("job_004");
        (bytes32 eventRoot, bytes32 receiptRoot, bytes32 deliveryHash) = _job004Roots();

        vm.prank(OPERATOR);
        anchor.anchorJob(jobId, eventRoot, receiptRoot, deliveryHash, 53_268_445);

        (bytes32 storedEvent,,,,) = anchor.anchors(jobId);

        // Recompute with one money movement altered (operator skimmed a cent).
        bytes32[] memory events = new bytes32[](6);
        events[0] = keccak256("JobFunded(job_004,25.00)");
        events[1] = keccak256("AdvanceIssued(job_004,12.50,0.25,5000)");
        events[2] = keccak256("ExpenseRecorded(job_004,0.04)");
        events[3] = keccak256("DeliverySubmitted(job_004,0xdeliv)");
        events[4] = keccak256("AdvanceRepaid(job_004,12.75)");
        events[5] = keccak256("JobSettled(job_004,pool=12.75,operator=12.26)"); // tampered
        bytes32 tamperedRoot = _fold(events);

        assertTrue(tamperedRoot != storedEvent, "tampered evidence must not match the anchor");
    }

    /// Leaf order is part of the commitment — the same events in a different order produce
    /// a different root, so an anchor pins the sequence, not just the set.
    function test_anchorJob_leafOrderIsPartOfTheRoot() public pure {
        bytes32 a = keccak256("A");
        bytes32 b = keccak256("B");
        bytes32[] memory forward = new bytes32[](2);
        forward[0] = a;
        forward[1] = b;
        bytes32[] memory reversed = new bytes32[](2);
        reversed[0] = b;
        reversed[1] = a;

        assertTrue(_fold(forward) != _fold(reversed), "order matters");
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-AA-001 — access control: only the operator authority may anchor
    // ─────────────────────────────────────────────────────────────────────

    function test_anchorJob_onlyOperatorAuthority() public {
        assertEq(anchor.operatorAuthority(), OPERATOR, "deployer is the authority");

        bytes32 jobId = keccak256("job_004");
        (bytes32 e, bytes32 r, bytes32 d) = _job004Roots();

        vm.expectRevert(AuditAnchor.NotAuthorized.selector);
        vm.prank(STRANGER);
        anchor.anchorJob(jobId, e, r, d, 1);

        (,,,, bool finalized) = anchor.anchors(jobId);
        assertFalse(finalized, "a rejected caller leaves the slot un-anchored");
    }

    function test_anchorJob_revertsForContractDeployerActingAsStranger() public {
        // The test contract deployed nothing here — OPERATOR did — so address(this) is
        // just another unauthorized caller.
        bytes32 jobId = keccak256("job_x");
        vm.expectRevert(AuditAnchor.NotAuthorized.selector);
        anchor.anchorJob(jobId, bytes32("e"), bytes32("r"), bytes32("d"), 1);
    }

    // ─────────────────────────────────────────────────────────────────────
    // SC-AA-002 — immutable once finalized: re-anchoring the SAME job is rejected
    // ─────────────────────────────────────────────────────────────────────

    /// Decision under test: re-anchoring a finalized job is REJECTED, not overwritten.
    /// This is the correct behaviour — an audit anchor that could be silently rewritten
    /// would let the operator restate history after the fact. The contract's own comment
    /// says corrections are a new version event, i.e. a new jobId (see the versioned test
    /// below), never a mutation of the existing slot.
    function test_anchorJob_reAnchorSameJobReverts() public {
        bytes32 jobId = keccak256("job_004");
        (bytes32 e, bytes32 r, bytes32 d) = _job004Roots();

        vm.prank(OPERATOR);
        anchor.anchorJob(jobId, e, r, d, 53_268_445);

        // Even the operator cannot overwrite it — with entirely different roots.
        vm.expectRevert(AuditAnchor.AlreadyFinalized.selector);
        vm.prank(OPERATOR);
        anchor.anchorJob(jobId, bytes32("new-event"), bytes32("new-receipt"), bytes32("new-deliv"), 99_999);

        // The original binding survives the rejected attempt, unchanged.
        (bytes32 se, bytes32 sr, bytes32 sd, uint64 sat,) = anchor.anchors(jobId);
        assertEq(se, e, "original event root survives");
        assertEq(sr, r, "original receipt root survives");
        assertEq(sd, d, "original delivery hash survives");
        assertEq(sat, 53_268_445, "original completedAt survives");
    }

    /// The sanctioned correction path: a restated job is anchored under a NEW versioned
    /// jobId, leaving the original slot intact. Both anchors coexist and are independently
    /// verifiable, which is what "corrections = new version event" means on chain.
    function test_anchorJob_correctionGoesToaNewVersionedJob() public {
        bytes32 v1 = keccak256(abi.encodePacked("job_004", uint8(1)));
        bytes32 v2 = keccak256(abi.encodePacked("job_004", uint8(2)));
        (bytes32 e, bytes32 r, bytes32 d) = _job004Roots();

        vm.startPrank(OPERATOR);
        anchor.anchorJob(v1, e, r, d, 53_268_445);
        anchor.anchorJob(v2, bytes32("corrected-event"), r, d, 53_268_500);
        vm.stopPrank();

        (bytes32 e1,,,, bool f1) = anchor.anchors(v1);
        (bytes32 e2,,,, bool f2) = anchor.anchors(v2);
        assertEq(e1, e, "v1 original untouched");
        assertEq(e2, bytes32("corrected-event"), "v2 holds the correction");
        assertTrue(f1 && f2, "both versions finalized and independently anchored");
    }

    // ─────────────────────────────────────────────────────────────────────
    // isolation — distinct jobs never collide
    // ─────────────────────────────────────────────────────────────────────

    function test_anchorJob_distinctJobsAreIndependent() public {
        bytes32 jobA = keccak256("job_004");
        bytes32 jobB = keccak256("job_005");

        vm.startPrank(OPERATOR);
        anchor.anchorJob(jobA, bytes32("evA"), bytes32("rcA"), bytes32("dvA"), 100);
        anchor.anchorJob(jobB, bytes32("evB"), bytes32("rcB"), bytes32("dvB"), 200);
        vm.stopPrank();

        (bytes32 ea,,, uint64 ta,) = anchor.anchors(jobA);
        (bytes32 eb,,, uint64 tb,) = anchor.anchors(jobB);
        assertEq(ea, bytes32("evA"), "job A root");
        assertEq(eb, bytes32("evB"), "job B root");
        assertEq(ta, 100, "job A completedAt");
        assertEq(tb, 200, "job B completedAt");

        // An unanchored job reads back as the zero anchor — the absence is legible.
        (bytes32 ez,,, uint64 tz, bool fz) = anchor.anchors(keccak256("job_never"));
        assertEq(ez, bytes32(0), "unanchored job has zero root");
        assertEq(tz, 0, "unanchored job has zero timestamp");
        assertFalse(fz, "unanchored job is not finalized");
    }
}
