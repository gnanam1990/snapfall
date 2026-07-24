// Per-job memory files (G4, FR-BRN-002, PRD §3).
//
// One file per job, atomically replaced on every update: scope, stage/completion %,
// assigned worker, every owner confirmation with its timestamp, escrow state. Job
// isolation is structural — each job's state lives in its own file, written under a
// per-store lock, and nothing here carries state from one job to another. This is
// Billing's source of truth and what Brain replays after a restart.
package brain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Confirmation is one owner decision, timestamped (FR-BRN-002).
type Confirmation struct {
	By   string    `json:"by"`
	What string    `json:"what"`
	At   time.Time `json:"at"`
}

// JobMemory is the entire per-job memory file.
type JobMemory struct {
	JobID          string         `json:"job_id"`
	Scope          string         `json:"scope"`
	QuoteUSDC      string         `json:"quote_usdc"`
	Stage          string         `json:"stage"`
	CompletionPct  int            `json:"completion_pct"`
	AssignedWorker string         `json:"assigned_worker,omitempty"`
	Confirmations  []Confirmation `json:"confirmations"`
	EscrowState    string         `json:"escrow_state"`
	Report         string         `json:"report,omitempty"`
	// G9: the QA trail — every bounce reason, the revision count, and the
	// evidence-not-guarantee disclaimer surfaced with any verdict.
	QANotes      []string `json:"qa_notes,omitempty"`
	QADisclaimer string   `json:"qa_disclaimer,omitempty"`
	// Draft is the author's deliverable under QA, persisted as JSON so a crash during
	// qa_review/revision recovers the actual content — without it, recovery would emit an
	// EMPTY delivery report (review fix, Anandan #4.3). Cleared once the report is sealed.
	Draft         string `json:"draft,omitempty"`
	RevisionCount int    `json:"revision_count,omitempty"`
	// VaultJobID is the bytes32 chain identity of this job — the join key Billing and
	// the settlement observer use. NO PRODUCER EXISTS today (the fourth face of the
	// chain gap: on-chain job creation never happens); it stays empty for real jobs
	// until the chain-write path lands. Tests set it directly.
	VaultJobID string `json:"vault_job_id,omitempty"`
	// Standing-pipeline identity. Set only for A11 milestone jobs; together these fields
	// make recovery and post-settlement chain observation independent of process memory.
	StandingInstructionID string `json:"standing_instruction_id,omitempty"`
	MilestoneNumber       uint64 `json:"milestone_number,omitempty"`
	// AcceptTokenHash is the SHA-256 (hex) of the customer's accept credential — the
	// plaintext is returned once at mint time and NEVER stored (pinned by test).
	AcceptTokenHash string    `json:"accept_token_hash,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// MemoryStore owns the directory of per-job files.
type MemoryStore struct {
	dir string
	mu  sync.Mutex
	// AfterUpdate, when set, observes every successful write with the NEW value —
	// Brain wires it to the jobs-table projection so no write site can forget to
	// project. Observers receive a copy and must not call back into the store.
	AfterUpdate func(JobMemory)
}

// NewMemoryStore creates the directory if needed.
func NewMemoryStore(dir string) (*MemoryStore, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("creating memory dir %s: %w", dir, err)
	}
	return &MemoryStore{dir: dir}, nil
}

func (m *MemoryStore) path(jobID string) string {
	// Job IDs are internal (job_104), but never trust a string that becomes a path.
	safe := strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(jobID)
	return filepath.Join(m.dir, safe+".json")
}

// Get loads one job's memory. Missing file = zero-valued memory, not an error.
func (m *MemoryStore) Get(jobID string) (JobMemory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load(jobID)
}

func (m *MemoryStore) load(jobID string) (JobMemory, error) {
	raw, err := os.ReadFile(m.path(jobID))
	if os.IsNotExist(err) {
		return JobMemory{JobID: jobID}, nil
	}
	if err != nil {
		return JobMemory{}, err
	}
	var jm JobMemory
	if err := json.Unmarshal(raw, &jm); err != nil {
		return JobMemory{}, fmt.Errorf("corrupt memory file for %s: %w", jobID, err)
	}
	return jm, nil
}

// Update applies fn to one job's memory read-modify-write, under the store lock,
// and atomically replaces the file (temp + rename) so a crash mid-write can never
// leave a torn file behind.
func (m *MemoryStore) Update(jobID string, fn func(*JobMemory)) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	jm, err := m.load(jobID)
	if err != nil {
		return err
	}
	jm.JobID = jobID
	fn(&jm)
	jm.UpdatedAt = time.Now().UTC()

	raw, err := json.MarshalIndent(jm, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path(jobID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path(jobID)); err != nil {
		return err
	}
	if m.AfterUpdate != nil {
		m.AfterUpdate(jm)
	}
	return nil
}

// SetAssignedWorker records which worker kind serves the job.
func (m *MemoryStore) SetAssignedWorker(jobID, kind string) error {
	return m.Update(jobID, func(jm *JobMemory) {
		jm.AssignedWorker = kind
		jm.Stage = "assigned"
	})
}

// AddConfirmation appends one timestamped owner decision.
func (m *MemoryStore) AddConfirmation(jobID, by, what string) error {
	return m.Update(jobID, func(jm *JobMemory) {
		jm.Confirmations = append(jm.Confirmations, Confirmation{By: by, What: what, At: time.Now().UTC()})
	})
}

// List returns every job ID with a memory file, sorted.
func (m *MemoryStore) List() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if name, ok := strings.CutSuffix(e.Name(), ".json"); ok {
			ids = append(ids, name)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
