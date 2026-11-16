package purchasing

import (
	"context"
	"errors"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/budget"
)

// fakeScreen answers the payee check and records every address it was asked about.
type fakeScreen struct {
	hit   bool
	err   error
	asked []string
}

func (s *fakeScreen) Sanctioned(_ context.Context, address string) (bool, error) {
	s.asked = append(s.asked, address)
	return s.hit, s.err
}
func (s *fakeScreen) Provider() string { return "fake-oracle" }

// A listed payee is refused at the quote, before anything is committed: no approval,
// no budget hold, no payment. The screen sees the quote's payTo verbatim.
func TestPayeeScreen_SanctionedPayeeIsRefusedBeforeIntake(t *testing.T) {
	payer := &fakePayer{deliver: true}
	f := newPayFixture(t, payer)
	screen := &fakeScreen{hit: true}
	f.p.SetPayeeScreen(screen)

	out, err := f.p.Decide(context.Background(), payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "denied" || out.Code != "payee-sanctioned" {
		t.Fatalf("a sanctioned payee reported %+v; want denied/payee-sanctioned", out)
	}
	if len(screen.asked) != 1 || screen.asked[0] != payTestPayTo {
		t.Fatalf("screen asked about %v; want exactly the quoted payTo %s", screen.asked, payTestPayTo)
	}
	none(t, f.st, "approval.requested")
	none(t, f.st, budget.KindReserved)
	if pays, quotes, _ := payer.counts(); pays != 0 || quotes != 1 {
		t.Fatalf("the payer saw %d pay(s) and %d quote(s); want one quote and no payment", pays, quotes)
	}
}

// Not being able to screen is a refusal too: an unscreened payee is never paid.
func TestPayeeScreen_UnavailableScreenFailsClosed(t *testing.T) {
	payer := &fakePayer{deliver: true}
	f := newPayFixture(t, payer)
	f.p.SetPayeeScreen(&fakeScreen{err: errors.New("rpc down")})

	out, err := f.p.Decide(context.Background(), payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "denied" || out.Code != "payee-screening-unavailable" {
		t.Fatalf("an unavailable screen reported %+v; want denied/payee-screening-unavailable", out)
	}
	none(t, f.st, "approval.requested")
	none(t, f.st, budget.KindReserved)
	if pays, _, _ := payer.counts(); pays != 0 {
		t.Fatalf("the payer saw %d payment(s) with the screen down; want none", pays)
	}
}

// A clean payee goes through exactly as it would with no screen wired.
func TestPayeeScreen_CleanPayeeProceeds(t *testing.T) {
	payer := &fakePayer{deliver: true}
	f := newPayFixture(t, payer)
	screen := &fakeScreen{}
	f.p.SetPayeeScreen(screen)

	out, err := f.p.Decide(context.Background(), payIntent(payTestAmountMicros))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "delivered" {
		t.Fatalf("a clean payee reported %+v; want delivered", out)
	}
	if len(screen.asked) != 1 {
		t.Fatalf("screen asked %d times; want once per quote", len(screen.asked))
	}
	if pays, _, _ := payer.counts(); pays != 1 {
		t.Fatalf("the payer saw %d payment(s); want one", pays)
	}
}

// With no payee address on the quote (no payment lane wired) there is nothing to screen
// and the flow keeps its stub shape: the screen is never consulted.
func TestPayeeScreen_NoLaneMeansNothingToScreen(t *testing.T) {
	f := newPayFixture(t, nil)
	screen := &fakeScreen{hit: true}
	f.p.SetPayeeScreen(screen)

	if _, err := f.p.Decide(context.Background(), payIntent(payTestAmountMicros)); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if len(screen.asked) != 0 {
		t.Fatalf("screen was asked %v with no payee on the quote", screen.asked)
	}
}
