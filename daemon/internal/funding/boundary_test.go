// The one-layer-out proofs the Step-6 review demanded: with wiring as the trust
// boundary, prove wiring CANNOT invoke funding without an approval-minted Grant.
package funding_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
)

// The sole mutating entry point demands approval.Grant. Reflection over the FULL
// method set: any new method added to the Agent must be classified here, so a
// second door cannot appear silently.
func TestFunding_SoleEntryPointDemandsAGrant(t *testing.T) {
	typ := reflect.TypeOf(&funding.Agent{})

	allowed := map[string]bool{
		"Execute":  true, // the door — signature checked below
		"Executed": true, // read-only inspection for Billing/tests
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
	g := approval.NewGrantForTest("req_1", "job_x", 4_000_000, "api.m.example")

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
