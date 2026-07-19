// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

/// @title AuditAnchor — anchor job event roots on Arc (PRD §7.3, SC-AA-001..004)
/// @notice No plaintext customer data ever goes on-chain. Roots + hashes only.
contract AuditAnchor {
    struct Anchor {
        bytes32 eventRoot;
        bytes32 paymentReceiptRoot;
        bytes32 deliveryHash;
        uint64  completedAt;
        bool    finalized;
    }

    address public operatorAuthority;
    mapping(bytes32 => Anchor) public anchors;

    event JobAnchored(bytes32 indexed jobId, bytes32 eventRoot, bytes32 paymentReceiptRoot, bytes32 deliveryHash, uint64 completedAt);

    error NotAuthorized();
    error AlreadyFinalized();

    constructor() { operatorAuthority = msg.sender; }

    /// SC-AA-001 operator-only; SC-AA-002 immutable once finalized (corrections = new version event)
    function anchorJob(bytes32 jobId, bytes32 eventRoot, bytes32 paymentReceiptRoot, bytes32 deliveryHash, uint64 completedAt) external {
        if (msg.sender != operatorAuthority) revert NotAuthorized();
        if (anchors[jobId].finalized) revert AlreadyFinalized();
        anchors[jobId] = Anchor(eventRoot, paymentReceiptRoot, deliveryHash, completedAt, true);
        emit JobAnchored(jobId, eventRoot, paymentReceiptRoot, deliveryHash, completedAt);
    }
}
