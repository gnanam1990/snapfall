package testnetops

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeDeploymentClock struct {
	deployedAt time.Time
	chainNow   time.Time
	err        error
}

func (f fakeDeploymentClock) BlockTimestamp(context.Context, uint64) (time.Time, error) {
	return f.deployedAt, f.err
}

func (f fakeDeploymentClock) LatestBlockTimestamp(context.Context) (time.Time, error) {
	return f.chainNow, f.err
}

func TestCheckRedeployCadenceAllowsDeploymentAfterFortyEightHours(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	err := CheckRedeployCadence(
		context.Background(),
		fakeDeploymentClock{deployedAt: now.Add(-48 * time.Hour), chainNow: now},
		53268443,
		time.Time{},
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
		fakeDeploymentClock{deployedAt: now.Add(-47*time.Hour - 59*time.Minute), chainNow: now},
		53268443,
		time.Time{},
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
		time.Time{},
		48*time.Hour,
	)
	if err == nil || !strings.Contains(err.Error(), "start block is zero") {
		t.Fatalf("expected zero-block failure, got %v", err)
	}
}

func TestCheckRedeployCadenceUsesChainHeadInsteadOfLocalClock(t *testing.T) {
	chainNow := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	err := CheckRedeployCadence(
		context.Background(),
		fakeDeploymentClock{deployedAt: chainNow.Add(-47 * time.Hour), chainNow: chainNow},
		53268443,
		time.Time{},
		48*time.Hour,
	)
	if err == nil || !strings.Contains(err.Error(), "has not elapsed") {
		t.Fatalf("expected chain-time cadence failure, got %v", err)
	}
}

func TestCheckRedeployCadenceUsesNewerSuccessfulBroadcastMarker(t *testing.T) {
	chainNow := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	err := CheckRedeployCadence(
		context.Background(),
		fakeDeploymentClock{deployedAt: chainNow.Add(-72 * time.Hour), chainNow: chainNow},
		53268443,
		chainNow.Add(-time.Hour),
		48*time.Hour,
	)
	if err == nil || !strings.Contains(err.Error(), "has not elapsed") {
		t.Fatalf("expected marker cadence failure, got %v", err)
	}
}

func TestRedeployMarkerRoundTrip(t *testing.T) {
	path := t.TempDir() + "/guard.json"
	at := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	if err := WriteRedeployMarker(path, 5042002, at); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRedeployMarker(path, 5042002)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(at) {
		t.Fatalf("marker = %s, want %s", got, at)
	}
}
