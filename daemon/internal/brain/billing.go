package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// SetBilling hands Brain the billing agent. Like Funding and the Purchaser, the pointer
// is held by Brain alone: workers cannot name the type (boundary law in the billing
// package) and the ONE agent-invocation site below is pinned by a source-scan test.
func (b *Brain) SetBilling(a *billing.Agent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.billingAgent = a
}

// GenerateInvoice is the SOLE billing invocation site in the daemon
// (TestG12_BillingInvocationSiteIsSingle). It builds the invoice exclusively from the
// job's chain record, contributes memory as labels + the Draft's provenance (never an
// amount), and appends the durable append-only-versioned billing.invoice event (H2 §4).
// trigger is "owner-request" (the exercisable path) or "settlement-observed" (written
// for the day the chain gap closes; has never fired against a real chain).
func (b *Brain) GenerateInvoice(ctx context.Context, jobID, trigger string) (billing.Record, error) {
	b.mu.Lock()
	agent := b.billingAgent
	b.mu.Unlock()
	if agent == nil {
		return billing.Record{}, errors.New("billing is not wired")
	}

	jm, err := b.memory.Get(jobID)
	if err != nil {
		return billing.Record{}, err
	}
	// A zero-valued memory means the daemon never touched this job: refuse rather than
	// emit an empty invoice for a name someone guessed.
	if jm.Scope == "" && jm.Stage == "" {
		return billing.Record{}, billing.ErrUnknownJob
	}
	var prov []envelope.SourceProvenance
	if jm.Draft != "" {
		var d envelope.Deliverable
		if err := json.Unmarshal([]byte(jm.Draft), &d); err == nil {
			prov = d.Provenance
		}
	}

	// Version assignment and the append are serialized so two concurrent generations
	// cannot claim the same version number.
	b.invoiceMu.Lock()
	defer b.invoiceMu.Unlock()
	var prior int
	if err := b.store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE kind='billing.invoice' AND entity_id=?`, jobID).Scan(&prior); err != nil {
		return billing.Record{}, fmt.Errorf("counting invoice versions: %w", err)
	}

	set, err := agent.Invoice(ctx, billing.Request{
		JobID:      jobID,
		VaultJobID: jm.VaultJobID,
		Labels:     billing.Labels{Title: jm.Scope},
		Provenance: prov,
	})
	if err != nil {
		return billing.Record{}, err
	}
	rec := billing.Record{
		Version: prior + 1, Trigger: trigger,
		Owner: set.Owner, Customer: set.Customer,
		Reconciliation: set.Reconciliation, Alerts: set.Alerts,
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return billing.Record{}, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return billing.Record{}, err
	}
	if _, err := b.store.Append(ctx, store.Event{
		Kind: "billing.invoice", EntityID: jobID, Actor: "billing", Payload: payload,
	}); err != nil {
		return billing.Record{}, err
	}
	b.log.Info("invoice generated", "job", jobID, "version", rec.Version, "trigger", trigger, "status", rec.Owner.Status)
	return rec, nil
}

// ObserveSettlementsOnce scans the shared store for JobSettled rows matching tracked
// jobs' vault ids and generates one settlement-observed invoice version per job.
//
// HONESTY: this path has never run against a real chain — no deployment exists, so no
// JobSettled row has ever been produced (the chain gap). It is written and tested
// against seeded rows in the same store the indexer writes, so it works the day
// settlement arrives instead of being discovered broken then. Today it also cannot
// match: nothing writes jobs' VaultJobID (the gap's fourth face).
//
// Idempotent per job: a job with an existing settlement-observed version is skipped —
// the watcher loop must not spam versions. Owner requests still append normally.
func (b *Brain) ObserveSettlementsOnce(ctx context.Context) (int, error) {
	b.mu.Lock()
	agent := b.billingAgent
	b.mu.Unlock()
	if agent == nil {
		return 0, nil
	}
	ids, err := b.memory.List()
	if err != nil {
		return 0, err
	}
	generated := 0
	for _, jobID := range ids {
		jm, err := b.memory.Get(jobID)
		if err != nil || jm.VaultJobID == "" {
			continue
		}
		var settled int
		if err := b.store.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM chain_events WHERE chain_id=? AND kind='JobSettled' AND entity_id=?`,
			agent.ChainID(), jm.VaultJobID).Scan(&settled); err != nil {
			return generated, err
		}
		if settled == 0 {
			continue
		}
		var already int
		if err := b.store.DB().QueryRowContext(ctx,
			`SELECT COUNT(*) FROM events WHERE kind='billing.invoice' AND entity_id=?
			 AND payload_json LIKE '%"trigger":"settlement-observed"%'`, jobID).Scan(&already); err != nil {
			return generated, err
		}
		if already > 0 {
			continue
		}
		if _, err := b.GenerateInvoice(ctx, jobID, "settlement-observed"); err != nil {
			return generated, err
		}
		generated++
	}
	return generated, nil
}
