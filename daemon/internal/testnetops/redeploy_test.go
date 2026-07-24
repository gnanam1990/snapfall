package testnetops

import (
	"context"
	"os"
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

func TestRedeployReservationRejectsConcurrentProcess(t *testing.T) {
	path := t.TempDir() + "/guard.pending.json"
	first, err := AcquireRedeployReservation(path, 5042002)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Release(); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	})

	if _, err := AcquireRedeployReservation(path, 5042002); err == nil ||
		!strings.Contains(err.Error(), "already pending") {
		t.Fatalf("expected concurrent reservation failure, got %v", err)
	}
}

func TestRedeployReservationPersistsUntilExplicitRelease(t *testing.T) {
	path := t.TempDir() + "/guard.pending.json"
	reservation, err := AcquireRedeployReservation(path, 5042002)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("pending reservation was not persisted: %v", err)
	}
	if err := reservation.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("pending reservation still exists after release: %v", err)
	}
}
