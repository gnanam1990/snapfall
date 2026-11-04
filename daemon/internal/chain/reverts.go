package chain

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

// contractErrors are the custom errors Snapfall's contracts revert with. All of them take no
// arguments, so the 4-byte selector is the whole revert payload. TestContractErrorsMatchSources
// pins this list to contracts/src: a new error has to be named here or the test fails.
var contractErrors = []string{
	// FloatPool
	"AdminTransferTimelocked", "AdvanceTooLarge", "AlreadyWired", "CapExceeded", "CapNotSet",
	"CapRaiseTimelocked", "DepositorNotAllowed", "DuplicateAdvance", "EnforcedPause",
	"ExposureCapExceeded", "InsufficientLiquidity", "JobNotFunded", "MixedCapChange",
	"NoCapRaiseQueued", "NoOpenAdvance", "NotAuthorized", "NotJobVault", "NotPendingAdmin",
	"NotTreasury", "NotWired", "WrongRepayment", "ZeroAddress", "ZeroAmount",
	// JobVault (beyond the shared names above)
	"AlreadyFunded", "InvalidStatus", "JobExists", "JobTooLarge", "OverBudget", "UnknownJob", "ZeroHash",
	// AuditAnchor
	"AlreadyFinalized",
}

var errorBySelector = func() map[[4]byte]string {
	m := make(map[[4]byte]string, len(contractErrors))
	for _, name := range contractErrors {
		var sel [4]byte
		copy(sel[:], selector(name+"()"))
		m[sel] = name + "()"
	}
	return m
}()

// RevertName names a revert payload from one of Snapfall's contracts, e.g. "ExposureCapExceeded()".
func RevertName(data []byte) (string, bool) {
	if len(data) != 4 {
		return "", false
	}
	var sel [4]byte
	copy(sel[:], data)
	name, ok := errorBySelector[sel]
	return name, ok
}

// revertSuffix turns a JSON-RPC error's data field into text for the error message. A node that
// rejects eth_call or eth_estimateGas puts the revert payload there; without it, "execution
// reverted" says nothing about WHICH check refused (a paused pool, a spent loss budget, a
// duplicate advance). Data that is not a hex string is left out rather than guessed at.
func revertSuffix(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || len(s) < 10 || s[:2] != "0x" {
		return ""
	}
	data := common.FromHex(s)
	if name, ok := RevertName(data); ok {
		return ": " + name
	}
	if len(data) > 36 {
		return fmt.Sprintf(" (revert data 0x%x…)", data[:36])
	}
	return fmt.Sprintf(" (revert data 0x%x)", data)
}
