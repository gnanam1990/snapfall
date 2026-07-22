// Command freeze-demo shows the G11 kill switch live (Step-B manual verification):
//
//  1. a freeze engaged mid-job — task refused, reads still working
//
//  2. the kill → freeze → restart → unfreeze intersection, with the event log
//
//  3. the owner-facing freeze report, including the in-flight note
//
//     go run ./cmd/freeze-demo
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

func banner(s string) {
	fmt.Printf("\n══ %s ══════════════════════════════\n", s)
}

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "snapfall-freeze-demo")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	// ── Part 1: freeze mid-job — task refused, read still works. ──
	banner("PART 1 — freeze engaged mid-job")
	st, err := store.Open(ctx, filepath.Join(dir, "part1.db"))
	if err != nil {
		panic(err)
	}
	mem, _ := brain.NewMemoryStore(filepath.Join(dir, "jobs"))
	b := brain.New(slog.New(slog.NewTextHandler(io.Discard, nil)), st, mem, funding.New())
	b.SetScoper(brain.StubScoper{})
	b.RegisterWorker(worker.StubDD{})
	reg, err := freeze.NewRegistry(ctx, st, time.Now)
	if err != nil {
		panic(err)
	}
	b.SetFreeze(reg, "org_demo")

	b.HandleOwnerRequest(ctx, "job_live", "Acme Corp")
	fmt.Println("   job scoped; owner is about to confirm...")

	reg.Engage(ctx, freeze.KindJob, "job_live", "gnanam", "suspicious activity")
	fmt.Println("   >>> KILL SWITCH: job_live frozen by gnanam")

	if err := b.Confirm(ctx, "job_live", "gnanam"); err != nil {
		fmt.Printf("   task dispatch REFUSED: %v\n", err)
	}
	if js, ok := b.Job("job_live"); ok {
		fmt.Printf("   read STILL WORKS while frozen: stage=%s scope=%q\n", js.Stage, js.Scope)
	}
	st.Close()

	// ── Part 2: kill → freeze → restart → unfreeze, with the event log. ──
	banner("PART 2 — kill, freeze, restart, unfreeze (the pin-3 intersection)")
	dbPath := filepath.Join(dir, "part2.db")

	mk := func(st2 *store.Store) (*approval.Lifecycle, *freeze.Registry, *funding.Agent) {
		l := approval.New(st2, time.Now)
		l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
		l.Spend = func(string) policy.SpendState { return policy.SpendState{} }
		r, err := freeze.NewRegistry(ctx, st2, time.Now)
		if err != nil {
			panic(err)
		}
		r.InFlightProbe = l.InFlight
		l.Freeze = r
		return l, r, funding.New()
	}
	mkIntent := func(id, nb string) approval.Intent {
		return approval.Intent{
			IntentID: id, OrgID: "org_demo", JobID: "job_x", TaskID: "t1",
			AgentID: "due-diligence", Merchant: policy.DemoMerchantPremium,
			Resource: "GET /v1/premium-dataset", AmountMicros: 4_000_000,
			Purpose: id, Nonce: "0x" + strings.Repeat(nb, 32),
			ExpiresAt: time.Now().Add(time.Hour),
		}
	}

	st1, _ := store.Open(ctx, dbPath)
	l1, r1, f1 := mk(st1)

	resA, _ := l1.Submit(ctx, mkIntent("pi_A", "aa"))
	l1.Decide(ctx, resA.Request.ID, approval.DecideApprove, "gnanam", "")
	l1.Execute(ctx, resA.Request.Intent, resA.Request.ID, f1.Execute)
	fmt.Println("   payment A: approved + EXECUTED")

	resB, _ := l1.Submit(ctx, mkIntent("pi_B", "bb"))
	l1.Decide(ctx, resB.Request.ID, approval.DecideApprove, "gnanam", "")
	fmt.Println("   payment B: approved, NOT executed")

	r1.Engage(ctx, freeze.KindOrg, "org_demo", "gnanam", "incident: freezing before restart")
	fmt.Println("   >>> FREEZE engaged (org scope)")
	st1.Close()
	fmt.Println("   >>> DAEMON KILLED")

	st2, _ := store.Open(ctx, dbPath)
	defer st2.Close()
	l2, r2, f2 := mk(st2)
	l2.Recover(ctx)
	fmt.Println("   >>> RESTARTED: lifecycle + freeze replayed from the event log")

	if e := r2.Check("org_demo", "", ""); e != nil {
		fmt.Printf("   freeze SURVIVED restart: %s %q by %s (%s)\n", e.Kind, e.ID, e.By, e.Reason)
	}
	if err := l2.Execute(ctx, resB.Request.Intent, resB.Request.ID, f2.Execute); err != nil {
		fmt.Printf("   B while frozen: REFUSED — %v\n", err)
	}

	r2.Lift(ctx, freeze.KindOrg, "org_demo", "gnanam", "incident resolved")
	fmt.Println("   >>> UNFREEZE (audited)")

	if err := l2.Execute(ctx, resB.Request.Intent, resB.Request.ID, f2.Execute); err == nil {
		fmt.Println("   B after unfreeze: EXECUTED (not lost)")
	}
	if err := l2.Execute(ctx, resA.Request.Intent, resA.Request.ID, f2.Execute); err != nil {
		fmt.Printf("   A replay attempt: REFUSED — %v\n", err)
	}

	fmt.Println("\n   event log (payments + freeze):")
	rows, _ := st2.DB().Query(`SELECT seq, kind, actor FROM events
		WHERE kind LIKE 'payment.%' OR kind LIKE 'freeze.%' ORDER BY seq`)
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var kind, actor string
		rows.Scan(&seq, &kind, &actor)
		fmt.Printf("   %3d  %-24s %s\n", seq, kind, actor)
	}

	// ── Part 3: the owner-facing report with the in-flight note. ──
	banner("PART 3 — the owner-facing freeze report (in-flight visibility)")
	st3, _ := store.Open(ctx, filepath.Join(dir, "part3.db"))
	defer st3.Close()
	l3, r3, f3 := mk(st3)

	res, _ := l3.Submit(ctx, mkIntent("pi_C", "cc"))
	l3.Decide(ctx, res.Request.ID, approval.DecideApprove, "gnanam", "")
	l3.Execute(ctx, res.Request.Intent, res.Request.ID, func(c context.Context, g approval.Grant) error {
		// The owner hits the switch while this payment is mid-flight.
		r3.Engage(c, freeze.KindOrg, "org_demo", "gnanam", "PANIC: freeze everything")
		return f3.Execute(c, g)
	})

	rep, _ := json.MarshalIndent(r3.StatusReport(), "   ", "  ")
	fmt.Printf("   %s\n", rep)
}
