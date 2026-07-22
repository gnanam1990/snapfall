// Command approval-demo walks one intent through the G7 lifecycle with the state
// printed at every hop, then attempts the AT-05 substitution and prints the refusal
// (Step-4 manual verification).
//
//	go run ./cmd/approval-demo
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

func banner(s string) {
	fmt.Printf("\n══ %s ══════════════════════════════════════════\n", s)
}

func printRequest(l *approval.Lifecycle, id string) {
	r, ok := l.Request(id)
	if !ok {
		fmt.Println("   request: <none>")
		return
	}
	fmt.Printf("   request %s\n     state=%s decided_by=%q reason=%q executed=%v\n     intent_hash=%s\n",
		r.ID, r.State, r.DecidedBy, r.Reason, r.Executed, r.IntentHash)
}

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "snapfall-approval-demo")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(ctx, filepath.Join(dir, "approval-demo.db"))
	if err != nil {
		panic(err)
	}
	defer st.Close()

	l := approval.New(st, time.Now)
	l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	l.Spend = func(string) policy.SpendState { return policy.SpendState{} }

	executed := 0
	executor := func(_ context.Context, in approval.Intent) error {
		executed++
		fmt.Printf("   >>> EXECUTOR INVOKED (call %d): %s USDC -> %s\n",
			executed, policy.FormatUSDC(in.AmountMicros), in.Merchant)
		return nil
	}

	intent := approval.Intent{
		IntentID: "pi_demo_walk", OrgID: "org_demo", JobID: "job_104",
		TaskID: "task_research_01", AgentID: "due-diligence",
		Merchant: policy.DemoMerchantPremium, Resource: "GET /v1/premium-dataset",
		AmountMicros: 4_000_000, Purpose: "premium market dataset",
		Nonce:     "0x" + strings.Repeat(fmt.Sprintf("%02x", time.Now().Unix()%256), 32),
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	// ── Hop 1: REQUEST ──
	banner("HOP 1 — Submit: the $4.00 intent enters the lifecycle")
	res, err := l.Submit(ctx, intent)
	if err != nil {
		panic(err)
	}
	fmt.Printf("   policy outcome: %s\n   reason: %s\n", res.Decision.Outcome, res.Decision.Reason.Message)
	printRequest(l, res.Request.ID)

	fmt.Println("\n   executing BEFORE approval (must refuse):")
	if err := l.Execute(ctx, res.Request.Intent, res.Request.ID, executor); err != nil {
		fmt.Printf("   REFUSED: %v\n", err)
	}
	fmt.Printf("   executor invocations so far: %d\n", executed)

	// ── Hop 2: APPROVE ──
	banner("HOP 2 — Decide: the owner approves")
	req, err := l.Decide(ctx, res.Request.ID, approval.DecideApprove, "gnanam", "needed for the report")
	if err != nil {
		panic(err)
	}
	printRequest(l, req.ID)

	// ── Hop 3: EXECUTE ──
	banner("HOP 3 — Execute: the approved intent runs exactly once")
	if err := l.Execute(ctx, req.Intent, req.ID, executor); err != nil {
		panic(err)
	}
	printRequest(l, req.ID)

	fmt.Println("\n   executing AGAIN (must refuse — exactly once):")
	if err := l.Execute(ctx, req.Intent, req.ID, executor); err != nil {
		fmt.Printf("   REFUSED: %v\n", err)
	}
	fmt.Printf("   executor invocations total: %d\n", executed)

	// ── AT-05: the substitution attempt ──
	banner("AT-05 — approve, then change a parameter, then try to execute")
	subIntent := intent
	subIntent.IntentID = "pi_demo_at05"
	subIntent.Nonce = "0x" + strings.Repeat("77", 32)
	res2, err := l.Submit(ctx, subIntent)
	if err != nil {
		panic(err)
	}
	l.Decide(ctx, res2.Request.ID, approval.DecideApprove, "gnanam", "approved at 4.00")
	fmt.Println("   approved at 4.000000 USDC to " + subIntent.Merchant)

	tampered := res2.Request.Intent
	tampered.AmountMicros = 4_000_001 // one micro more than approved
	fmt.Println("\n   attacker raises the amount by ONE micro-USDC and presents the old approval:")
	if err := l.Execute(ctx, tampered, res2.Request.ID, executor); err != nil {
		fmt.Printf("   REFUSED: %v\n", err)
	}

	tampered2 := res2.Request.Intent
	tampered2.Merchant = "api.attacker.example"
	fmt.Println("\n   attacker swaps the merchant, amount untouched:")
	if err := l.Execute(ctx, tampered2, res2.Request.ID, executor); err != nil {
		fmt.Printf("   REFUSED: %v\n", err)
	}

	fmt.Println("\n   the UNTAMPERED intent still executes fine:")
	if err := l.Execute(ctx, res2.Request.Intent, res2.Request.ID, executor); err != nil {
		panic(err)
	}
	fmt.Printf("\n   executor invocations total: %d (hop 3 + the untampered AT-05 intent)\n", executed)
}
