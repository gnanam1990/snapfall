// Package indexer is the H1 Arc-to-SQLite module (A2/A3).
//
// Its external interface is deliberately one operation: SyncOnce. Polling, ordering, ABI
// decoding, replay protection, projection and cursor advancement remain implementation details.
package indexer

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Source is the true-external seam at Arc JSON-RPC. RPCSource is the production adapter;
// tests use an in-memory adapter to exercise the same SyncOnce interface.
type Source interface {
	ChainID(context.Context) (uint64, error)
	Head(context.Context) (uint64, error)
	Logs(context.Context, Filter) ([]Log, error)
}

// Filter is one bounded eth_getLogs request.
type Filter struct {
	FromBlock uint64
	ToBlock   uint64
	Addresses []string
}

// Log is the RPC-neutral representation of an EVM log.
type Log struct {
	Address         string
	Topics          []string
	Data            string
	BlockNumber     uint64
	BlockHash       string
	TransactionHash string
	LogIndex        uint64
	Removed         bool
}

// Config fixes the chain identity and replay window for one Indexer.
type Config struct {
	ChainID           uint64
	Addresses         []string
	StartBlock        uint64
	ConfirmationDepth uint64
	ChunkSize         uint64
}

// Result describes newly committed work, not merely logs returned by the RPC. Replaying an
// already-indexed range therefore reports zero RawLogs and Events.
type Result struct {
	FromBlock    uint64
	ThroughBlock uint64
	NextBlock    uint64
	RawLogs      int
	Events       int
}

// Indexer hides the complete H1 implementation behind SyncOnce.
type Indexer struct {
	source        Source
	store         *store.Store
	cfg           Config
	mu            sync.Mutex
	chainVerified bool
}

// New validates the immutable indexer configuration.
func New(source Source, st *store.Store, cfg Config) (*Indexer, error) {
	if source == nil || st == nil {
		return nil, fmt.Errorf("indexer source and store are required")
	}
	if cfg.ChainID == 0 {
		return nil, fmt.Errorf("chain ID must be non-zero")
	}
	if len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("at least one contract address is required")
	}
	seen := make(map[string]bool, len(cfg.Addresses))
	for i, address := range cfg.Addresses {
		normalized, err := normalizeAddress(address)
		if err != nil {
			return nil, fmt.Errorf("address %d: %w", i, err)
		}
		if seen[normalized] {
			return nil, fmt.Errorf("duplicate contract address %s", normalized)
		}
		seen[normalized] = true
		cfg.Addresses[i] = normalized
	}
	if cfg.ChunkSize == 0 {
		cfg.ChunkSize = 2_000
	}
	return &Indexer{source: source, store: st, cfg: cfg}, nil
}

func normalizeAddress(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != 42 || !strings.HasPrefix(v, "0x") {
		return "", fmt.Errorf("%q is not a 20-byte 0x address", v)
	}
	for _, c := range v[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%q contains non-hex characters", v)
		}
	}
	return v, nil
}

func normalizeHash32(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != 66 || !strings.HasPrefix(v, "0x") {
		return "", fmt.Errorf("%q is not 32-byte 0x hex", v)
	}
	if _, err := hex.DecodeString(v[2:]); err != nil {
		return "", fmt.Errorf("%q contains non-hex characters", v)
	}
	return v, nil
}

func normalizeData(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if !strings.HasPrefix(v, "0x") || len(v)%2 != 0 {
		return "", fmt.Errorf("log data %q is not even-length 0x hex", v)
	}
	if _, err := hex.DecodeString(v[2:]); err != nil {
		return "", fmt.Errorf("log data contains non-hex characters: %w", err)
	}
	return v, nil
}
