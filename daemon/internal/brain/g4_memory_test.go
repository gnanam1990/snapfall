package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// G4 gate: two concurrent jobs show ZERO context bleed across their memory files.

// slowScoper scopes distinctly per request so bleed is detectable, and yields to the
// scheduler to maximize interleaving.
type slowScoper struct{}

func (slowScoper) Scope(_ context.Context, request string) (string, string, string, error) {
	return "DD scope for " + request, "25.00", "due-diligence", nil
}

// G4: an interleaved sequence of updates to two jobs, then byte-level inspection of
// both memory files for any trace of the other job.
func TestG4_ConcurrentJobsZeroContextBleed(t *testing.T) {
	memDir := filepath.Join(t.TempDir(), "jobs")
	b, _, _ := newTestBrainWithMemDir(t, memDir)
	b.SetScoper(slowScoper{})
	ctx := context.Background()

	// The two jobs carry deliberately distinctive markers.
	jobs := map[string]string{
		"job_alpha": "ALPHACORP-SECRET-A",
		"job_beta":  "BETACORP-SECRET-B",
	}

	// Interleave the full lifecycle of both jobs across goroutines.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for jobID, marker := range jobs {
		wg.Add(1)
		go func(jobID, marker string) {
			defer wg.Done()
			if _, err := b.HandleOwnerRequest(ctx, jobID, marker); err != nil {
				errs <- fmt.Errorf("%s request: %w", jobID, err)
				return
			}
			if err := b.Confirm(ctx, jobID, "owner-"+jobID); err != nil {
				errs <- fmt.Errorf("%s confirm: %w", jobID, err)
				return
			}
			waitJob(b, jobID) // await the async task before asserting the terminal state
		}(jobID, marker)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	// Both completed independently.
	for jobID := range jobs {
		jm, err := b.memory.Get(jobID)
		if err != nil {
			t.Fatalf("Get %s: %v", jobID, err)
		}
		if jm.Stage != string(StageComplete) || jm.CompletionPct != 100 {
			t.Errorf("%s: stage %s pct %d, want complete/100", jobID, jm.Stage, jm.CompletionPct)
		}
		if len(jm.Confirmations) != 1 {
			t.Fatalf("%s: %d confirmations, want exactly 1", jobID, len(jm.Confirmations))
		}
		if jm.Confirmations[0].By != "owner-"+jobID {
			t.Errorf("%s: confirmation by %q — another job's confirmation leaked in", jobID, jm.Confirmations[0].By)
		}
		if jm.Confirmations[0].At.IsZero() {
			t.Errorf("%s: confirmation carries no timestamp (FR-BRN-002)", jobID)
		}
	}

	// The bleed check, at the byte level: neither job's file may contain the other
	// job's marker, ID, or owner — anywhere, in any field.
	for jobID := range jobs {
		raw, err := os.ReadFile(filepath.Join(memDir, jobID+".json"))
		if err != nil {
			t.Fatalf("reading %s memory file: %v", jobID, err)
		}
		for otherID, otherMarker := range jobs {
			if otherID == jobID {
				continue
			}
			for _, foreign := range []string{otherMarker, otherID, "owner-" + otherID} {
				if strings.Contains(string(raw), foreign) {
					t.Errorf("CONTEXT BLEED: %s's memory file contains %q", jobID, foreign)
				}
			}
		}
	}
}

// G4: memory files survive a Brain restart — a fresh MemoryStore over the same
// directory reads back exactly what was written (the replay substrate for AT-10).
func TestG4_MemoryFilesSurviveRestart(t *testing.T) {
	memDir := filepath.Join(t.TempDir(), "jobs")
	b, _, _ := newTestBrainWithMemDir(t, memDir)
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_r", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_r", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	waitJob(b, "job_r")

	// "Restart": a brand-new MemoryStore over the same dir.
	mem2, err := NewMemoryStore(memDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	jm, err := mem2.Get("job_r")
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if jm.Stage != string(StageComplete) || len(jm.Confirmations) != 1 || jm.Report == "" {
		t.Errorf("state lost across restart: %+v", jm)
	}

	ids, err := mem2.List()
	if err != nil || len(ids) != 1 || ids[0] != "job_r" {
		t.Errorf("List after restart = %v (%v), want [job_r]", ids, err)
	}
}

// G4: a torn write is impossible — files go through temp+rename, so a reader always
// sees a complete JSON document.
func TestG4_MemoryFileIsAlwaysWholeJSON(t *testing.T) {
	memDir := filepath.Join(t.TempDir(), "jobs")
	mem, err := NewMemoryStore(memDir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Hammer updates...
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				mem.Update("job_t", func(jm *JobMemory) { jm.CompletionPct = i % 101 })
			}
		}
	}()

	// ...while reading the file raw, repeatedly. Every read must parse.
	for i := 0; i < 200; i++ {
		raw, err := os.ReadFile(filepath.Join(memDir, "job_t.json"))
		if os.IsNotExist(err) {
			continue // first write may not have landed yet
		}
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		var jm JobMemory
		if err := json.Unmarshal(raw, &jm); err != nil {
			t.Fatalf("read %d saw a torn file: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
}

// newTestBrainWithMemDir mirrors newTestBrain but pins the memory directory.
func newTestBrainWithMemDir(t *testing.T, memDir string) (*Brain, *MemoryStore, error) {
	t.Helper()
	b, _, _ := newTestBrain(t)
	mem, err := NewMemoryStore(memDir)
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	b.memory = mem
	return b, mem, nil
}
