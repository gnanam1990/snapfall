package testnetops

import (
	"context"
	"fmt"
	"time"
)

// DeploymentClock reads the timestamp of a deployment block.
type DeploymentClock interface {
	BlockTimestamp(context.Context, uint64) (time.Time, error)
}

// CheckRedeployCadence rejects accidental redeploys before the configured cadence has elapsed.
func CheckRedeployCadence(
	ctx context.Context,
	source DeploymentClock,
	startBlock uint64,
	now time.Time,
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
	age := now.Sub(deployedAt)
	if age < 0 {
		return fmt.Errorf("deployment block timestamp %s is in the future", deployedAt.UTC().Format(time.RFC3339))
	}
	if age < cadence {
		return fmt.Errorf(
			"deployment is only %s old; the %s redeploy cadence has not elapsed",
			age.Round(time.Minute), cadence,
		)
	}
	return nil
}
