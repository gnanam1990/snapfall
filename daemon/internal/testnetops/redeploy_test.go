package testnetops

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeDeploymentClock struct {
	deployedAt time.Time
	err        error
}

func (f fakeDeploymentClock) BlockTimestamp(context.Context, uint64) (time.Time, error) {
	return f.deployedAt, f.err
}

func TestCheckRedeployCadenceAllowsDeploymentAfterFortyEightHours(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	err := CheckRedeployCadence(
		context.Background(),
		fakeDeploymentClock{deployedAt: now.Add(-48 * time.Hour)},
		53268443,
		now,
		48*time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckRedeployCadenceRejectsEarlyDeployment(t *testing.T) {
	now := time.Date(2026, 7, 25, 11, 59, 0, 0, time.UTC)
	err := CheckRedeployCadence(
		context.Background(),
		fakeDeploymentClock{deployedAt: now.Add(-47*time.Hour - 59*time.Minute)},
		53268443,
		now,
		48*time.Hour,
	)
	if err == nil || !strings.Contains(err.Error(), "has not elapsed") {
		t.Fatalf("expected cadence failure, got %v", err)
	}
}

func TestCheckRedeployCadenceRejectsZeroStartBlock(t *testing.T) {
	err := CheckRedeployCadence(
		context.Background(),
		fakeDeploymentClock{},
		0,
		time.Now(),
		48*time.Hour,
	)
	if err == nil || !strings.Contains(err.Error(), "start block is zero") {
		t.Fatalf("expected zero-block failure, got %v", err)
	}
}
