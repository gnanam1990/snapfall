package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Serve pin 1, the daemon half: EXACTLY ONE Brain is constructed in this binary, so
// exactly one Recover runs over the event log. A second brain.New site would race two
// replays of the same state — the double-recovery hazard from #4. Same source-scan
// technique as the dispatch-chokepoint and StageDeliveryReady pins.
func TestMain_SingleBrainWiringSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`brain\.New\(`)
	sites := 0
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(re.FindAll(raw, -1)); n > 0 {
			sites += n
			files = append(files, e.Name())
		}
	}
	if sites != 1 {
		t.Fatalf("brain.New sites = %d in %v, want exactly 1 (wireBrain) — a second Brain races a second Recover over the same event log", sites, files)
	}
}

// G10's recorded-path pin: the daemon wires the DISCOVERY-driven DD worker from exactly
// one site. The scripted source plan does not merely go unwired — its constructor and
// plan type were deleted, so outside the worker package the scripted path cannot be
// named at all; this scan pins the one place the only remaining path is constructed,
// so the demo cannot accidentally record anything else.
func TestMain_SingleDiscoveryWiringSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`worker\.NewDiscoveryDD\(`)
	sites := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sites += len(re.FindAll(raw, -1))
	}
	if sites != 1 {
		t.Fatalf("discovery-DD wiring sites = %d, want exactly 1", sites)
	}
}
