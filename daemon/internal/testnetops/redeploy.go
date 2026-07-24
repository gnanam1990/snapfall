package testnetops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DeploymentClock reads the timestamp of a deployment block.
type DeploymentClock interface {
	BlockTimestamp(context.Context, uint64) (time.Time, error)
	LatestBlockTimestamp(context.Context) (time.Time, error)
}

// CheckRedeployCadence rejects accidental redeploys before the configured cadence has elapsed.
func CheckRedeployCadence(
	ctx context.Context,
	source DeploymentClock,
	startBlock uint64,
	lastBroadcastAt time.Time,
	cadence time.Duration,
) error {
	if startBlock == 0 {
		return fmt.Errorf("deployment start block is zero; refusing an unguarded redeploy")
	}
	if cadence <= 0 {
		return fmt.Errorf("redeploy cadence must be positive")
	}
	deployedAt, err := source.BlockTimestamp(ctx, startBlock)
	if err != nil {
		return fmt.Errorf("reading deployment block %d: %w", startBlock, err)
	}
	chainNow, err := source.LatestBlockTimestamp(ctx)
	if err != nil {
		return fmt.Errorf("reading current chain time: %w", err)
	}
	reference := deployedAt
	if lastBroadcastAt.After(reference) {
		reference = lastBroadcastAt
	}
	age := chainNow.Sub(reference)
	if age < 0 {
		return fmt.Errorf("redeployment reference timestamp %s is ahead of chain time", reference.UTC().Format(time.RFC3339))
	}
	if age < cadence {
		return fmt.Errorf(
			"deployment is only %s old; the %s redeploy cadence has not elapsed",
			age.Round(time.Minute), cadence,
		)
	}
	return nil
}

type redeployMarker struct {
	ChainID     uint64 `json:"chainId"`
	BroadcastAt string `json:"broadcastAt"`
}

// ReadRedeployMarker loads the durable guard written immediately after a successful
// broadcast. A stale deployment artifact therefore cannot authorize an immediate repeat.
func ReadRedeployMarker(path string, expectedChainID uint64) (time.Time, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("reading redeploy guard: %w", err)
	}
	var marker redeployMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return time.Time{}, fmt.Errorf("decoding redeploy guard: %w", err)
	}
	if marker.ChainID != expectedChainID {
		return time.Time{}, fmt.Errorf(
			"redeploy guard chain ID %d does not match required chain ID %d",
			marker.ChainID, expectedChainID,
		)
	}
	at, err := time.Parse(time.RFC3339, marker.BroadcastAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid redeploy guard timestamp: %w", err)
	}
	return at, nil
}

// WriteRedeployMarker atomically records chain time after a successful broadcast.
func WriteRedeployMarker(path string, chainID uint64, broadcastAt time.Time) error {
	raw, err := json.Marshal(redeployMarker{
		ChainID: chainID, BroadcastAt: broadcastAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".redeploy-guard-*")
	if err != nil {
		return fmt.Errorf("creating redeploy guard: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("protecting redeploy guard: %w", err)
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return fmt.Errorf("writing redeploy guard: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("closing redeploy guard: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("installing redeploy guard: %w", err)
	}
	return nil
}
