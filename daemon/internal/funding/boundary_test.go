// The one-layer-out proofs the Step-6 review demanded: with wiring as the trust
// boundary, prove wiring CANNOT invoke funding without an approval-minted Grant.
package funding_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// mintRealGrant produces a legitimate Grant the only way one can exist: by driving a
// real approved lifecycle flow and capturing what the Executor is handed. There is no
// production constructor to shortcut this — that is the point of the capability boundary.
func mintRealGrant(t *testing.T) approval.Grant {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "grant.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	clock := func() time.Time { return time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC) }
	l := approval.New(st, clock)
	l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	l.Spend = func(string) policy.SpendState { return policy.SpendState{} }

	// $0.04 at an allowlisted merchant, below the 0.10 auto-approve threshold.
	in := approval.Intent{
		IntentID:     "pi_grant",
		OrgID:        "org_demo",
		JobID:        "job_x",
		TaskID:       "task_1",
		AgentID:      "due-diligence",
		Merchant:     policy.DemoMerchantProfile,
		Resource:     "GET /v1/company-profile",
		AmountMicros: 40_000,
		Purpose:      "company profile",
		Nonce:        "0x" + strings.Repeat("cd", 32),
		ExpiresAt:    clock().Add(5 * time.Minute),
	}
	res, err := l.Submit(ctx, in)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Decision.Outcome != policy.AutoApprove {
		t.Fatalf("intent did not auto-approve (outcome %s) — cannot mint a grant", res.Decision.Outcome)
	}
	var g approval.Grant
	if err := l.Execute(ctx, res.Request.Intent, res.Request.ID, func(_ context.Context, grant approval.Grant) error {
		g = grant
		return nil
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if g.Empty() {
		t.Fatal("captured grant is empty — the lifecycle did not mint it")
	}
	return g
}

// The sole mutating entry point demands approval.Grant. Reflection over the FULL
// method set: any new method added to the Agent must be classified here, so a
// second door cannot appear silently.
func TestFunding_SoleEntryPointDemandsAGrant(t *testing.T) {
	typ := reflect.TypeOf(&funding.Agent{})

	allowed := map[string]bool{
		"Execute":  true, // the door — signature checked below
		"Executed": true, // read-only inspection for Billing/tests
		// The chain lanes (classified at the chain-writer review, deliberately):
		"SetChain": true, // wiring-only setter: installs lanes, moves nothing
		// ExecuteAdvance DEMANDS the Grant like Execute and derives its calldata FROM
		// the grant's ChainRef — no caller-supplied calldata, no second door: an
		// attacker with the Agent pointer but no Grant gets the same refusal Execute
		// gives (empty-grant + replay checks run first, signature pinned below).
		"ExecuteAdvance": true,
		// SettleOnChain takes only a vault job id, NO Grant — a deliberate exception,
		// classified: the settlement principal is the CUSTOMER (no owner approval
		// exists to mint a Grant from), its authorization is upstream in Brain
		// (credential + state + freeze + exactly-once) and ON-CHAIN (SC-JV-005: the
		// contract refuses unless msg.sender is the job's customer and the job is
		// Delivered — a misdirected call reverts; it cannot misappropriate).
		"SettleOnChain": true,
	}
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if !allowed[name] {
			t.Errorf("funding.Agent grew a method %q — classify it: is it a second door past the Grant requirement?", name)
		}
	}

	exec, ok := typ.MethodByName("Execute")
	if !ok {
		t.Fatal("Execute missing")
	}
	// Params: receiver, context.Context, approval.Grant.
	if exec.Type.NumIn() != 3 {
		t.Fatalf("Execute takes %d params — the contract is (ctx, approval.Grant)", exec.Type.NumIn())
	}
	if got := exec.Type.In(2); got != reflect.TypeOf(approval.Grant{}) {
		t.Fatalf("Execute's credential parameter is %v, want approval.Grant — anything else reopens the bare-Decision door", got)
	}
}

// A forged (zero-value) Grant compiles but is refused, and nothing is recorded.
func TestFunding_RefusesForgedEmptyGrant(t *testing.T) {
	agent := funding.New()

	if err := agent.Execute(context.Background(), approval.Grant{}); err == nil {
		t.Fatal("an empty forged grant must be refused")
	}
	if got := len(agent.Executed()); got != 0 {
		t.Fatalf("refused grant was recorded: %d instruction(s)", got)
	}
}

// Review batch: a caller replaying the SAME legitimate grant must produce ONE movement,
// not many — belt-and-suspenders atop the lifecycle's exactly-once.
func TestFunding_RefusesGrantReplay(t *testing.T) {
	agent := funding.New()
	g := mintRealGrant(t)

	if err := agent.Execute(context.Background(), g); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if err := agent.Execute(context.Background(), g); err == nil {
		t.Fatal("replaying the same grant must be refused")
	}
	if got := len(agent.Executed()); got != 1 {
		t.Fatalf("grant replay produced %d movements, want 1", got)
	}
}

// ExecuteAdvance's signature demands the Grant exactly like Execute — pinned so the
// advance door can never drift to accepting anything forgeable.
func TestFunding_AdvanceDoorDemandsAGrantToo(t *testing.T) {
	m, ok := reflect.TypeOf(&funding.Agent{}).MethodByName("ExecuteAdvance")
	if !ok {
		t.Fatal("ExecuteAdvance missing")
	}
	grantType := reflect.TypeOf(approval.Grant{})
	found := false
	for i := 1; i < m.Type.NumIn(); i++ {
		if m.Type.In(i) == grantType {
			found = true
		}
	}
	if !found {
		t.Fatal("ExecuteAdvance does not take approval.Grant — the advance door lost the Grant requirement")
	}

	// And behaviorally: an empty (forged) grant is refused before any lane submission.
	if _, err := funding.New().ExecuteAdvance(context.Background(), approval.Grant{}); err == nil {
		t.Fatal("a forged grant reached the advance lane")
	}
}
