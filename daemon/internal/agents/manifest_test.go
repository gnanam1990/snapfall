package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeManifests materializes YAML files in a temp dir and returns its path.
func writeManifests(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}

const validResearch = `role: research
model: { provider: ollama, name: llama3.1 }
memory_namespace: job/{job_id}/research
filesystem_scope: ["workspace/{job_id}/**"]
command_allowlist: []
network_allowlist: ["our-paid-demo-api.local"]
budget_usdc: "1.00"
can_sign_payments: false
can_request_advance: false
escalates_to: manager
responsibilities: ["gather data"]
`

func findingCodes(fs []Finding) map[string]Finding {
	out := map[string]Finding{}
	for _, f := range fs {
		out[f.Code] = f
	}
	return out
}

// ─────────────────────────────────────────────────────────────────────
// The manifests we actually ship
// ─────────────────────────────────────────────────────────────────────

// The four committed manifests must load and activate. If this fails, the daemon
// cannot start, so it is the single most important test in the package.
func TestLoad_ShippedManifestsAreActivatable(t *testing.T) {
	ms, findings, err := Load("../../manifests")
	if err != nil {
		t.Fatalf("shipped manifests must validate, got: %v", err)
	}
	if len(ms) != 4 {
		t.Fatalf("expected 4 manifests (PRD §4.1 bounds the workforce at 4), got %d", len(ms))
	}

	roles := map[Role]bool{}
	for _, m := range ms {
		roles[m.Role] = true
	}
	for _, want := range []Role{RoleManager, RoleResearch, RoleDelivery, RoleFinance} {
		if !roles[want] {
			t.Errorf("missing role %q", want)
		}
	}

	// No shipped manifest may claim signing or borrowing authority.
	for _, m := range ms {
		if m.CanSignPayments {
			t.Errorf("%s claims can_sign_payments", m.Role)
		}
		if m.CanRequestAdv {
			t.Errorf("%s claims can_request_advance", m.Role)
		}
	}

	for _, f := range findings {
		if f.Fatal {
			t.Errorf("unexpected fatal finding: %s", f)
		}
	}
}

// Documents a real contradiction in the committed manifests: delivery carries a
// 0.10 USDC budget it can never spend, because its network allowlist is empty.
// Non-fatal by design — FR-ORG-006 says report, and a human decides.
func TestLoad_DeliveryBudgetIsUnreachable(t *testing.T) {
	_, findings, err := Load("../../manifests")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f, ok := findingCodes(findings)["budget-without-egress"]
	if !ok {
		t.Fatal("expected the delivery budget/egress contradiction to be reported")
	}
	if f.Fatal {
		t.Error("a contradiction is a warning, not an activation blocker")
	}
	if f.Role != RoleDelivery {
		t.Errorf("expected the finding on delivery, got %s", f.Role)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Architecture law — these must be fatal, not advisory
// ─────────────────────────────────────────────────────────────────────

// FR-PAY-001: agents never hold keys. A manifest asserting otherwise must not activate.
func TestValidate_RejectsAgentThatMaySign(t *testing.T) {
	dir := writeManifests(t, map[string]string{
		"rogue.yaml": strings.Replace(validResearch, "can_sign_payments: false", "can_sign_payments: true", 1),
	})
	_, findings, err := Load(dir)
	if err == nil {
		t.Fatal("a signing agent must fail validation")
	}
	if f, ok := findingCodes(findings)["agent-may-sign"]; !ok || !f.Fatal {
		t.Error("expected a fatal agent-may-sign finding")
	}
}

// FR-FLT-001 / SEC-011 / ADR-009: advances are human-authorized only.
// AT-15 requires an agent-originated advance to be rejected; this is the manifest-layer half.
func TestValidate_RejectsAgentThatMayBorrow(t *testing.T) {
	dir := writeManifests(t, map[string]string{
		"rogue.yaml": strings.Replace(validResearch, "can_request_advance: false", "can_request_advance: true", 1),
	})
	_, findings, err := Load(dir)
	if err == nil {
		t.Fatal("a borrowing agent must fail validation")
	}
	if f, ok := findingCodes(findings)["agent-may-borrow"]; !ok || !f.Fatal {
		t.Error("expected a fatal agent-may-borrow finding")
	}
}

// PRD §4.1: "no arbitrary shell". A shell in the command allowlist is arbitrary execution
// wearing an allowlist's clothes.
func TestValidate_RejectsShellInCommandAllowlist(t *testing.T) {
	for _, shell := range []string{"bash", "sh", "/bin/zsh", "sudo"} {
		dir := writeManifests(t, map[string]string{
			"rogue.yaml": strings.Replace(validResearch, `command_allowlist: []`,
				`command_allowlist: ["`+shell+`"]`, 1),
		})
		if _, findings, err := Load(dir); err == nil {
			t.Errorf("%q must be rejected", shell)
		} else if f, ok := findingCodes(findings)["shell-in-allowlist"]; !ok || !f.Fatal {
			t.Errorf("%q: expected a fatal shell-in-allowlist finding", shell)
		}
	}
}

// SEC-007: deny-by-default egress. A wildcard host is the opposite of an allowlist.
func TestValidate_RejectsWildcardEgress(t *testing.T) {
	for _, host := range []string{"*", "0.0.0.0/0", "ANY"} {
		dir := writeManifests(t, map[string]string{
			"rogue.yaml": strings.Replace(validResearch,
				`network_allowlist: ["our-paid-demo-api.local"]`,
				`network_allowlist: ["`+host+`"]`, 1),
		})
		if _, findings, err := Load(dir); err == nil {
			t.Errorf("%q must be rejected", host)
		} else if f, ok := findingCodes(findings)["wildcard-egress"]; !ok || !f.Fatal {
			t.Errorf("%q: expected a fatal wildcard-egress finding", host)
		}
	}
}

// A typo'd permission key must not silently fall back to the safe-looking zero value.
// `can_sign_payment: true` (missing the s) would otherwise parse as can_sign_payments=false
// and read as safe while the author believed they had granted signing.
func TestLoad_RejectsUnknownKeys(t *testing.T) {
	dir := writeManifests(t, map[string]string{
		"typo.yaml": strings.Replace(validResearch, "can_sign_payments: false", "can_sign_payment: true", 1),
	})
	if _, _, err := Load(dir); err == nil {
		t.Fatal("an unknown manifest key must be a parse error, not a silent default")
	}
}

// ─────────────────────────────────────────────────────────────────────
// Structural validity
// ─────────────────────────────────────────────────────────────────────

func TestValidate_RejectsUnknownRole(t *testing.T) {
	dir := writeManifests(t, map[string]string{
		"extra.yaml": strings.Replace(validResearch, "role: research", "role: marketing", 1),
	})
	if _, findings, err := Load(dir); err == nil {
		t.Fatal("a fifth role must be rejected (PRD §2.5 caps the workforce at 4)")
	} else if f, ok := findingCodes(findings)["unknown-role"]; !ok || !f.Fatal {
		t.Error("expected a fatal unknown-role finding")
	}
}

func TestValidate_RejectsDuplicateRole(t *testing.T) {
	dir := writeManifests(t, map[string]string{
		"a.yaml": validResearch,
		"b.yaml": validResearch,
	})
	if _, findings, err := Load(dir); err == nil {
		t.Fatal("two manifests claiming the same role must be rejected")
	} else if f, ok := findingCodes(findings)["duplicate-role"]; !ok || !f.Fatal {
		t.Error("expected a fatal duplicate-role finding")
	}
}

func TestValidate_RejectsSelfEscalation(t *testing.T) {
	dir := writeManifests(t, map[string]string{
		"loop.yaml": strings.Replace(validResearch, "escalates_to: manager", "escalates_to: research", 1),
	})
	if _, findings, err := Load(dir); err == nil {
		t.Fatal("self-escalation must be rejected")
	} else if f, ok := findingCodes(findings)["self-escalation"]; !ok || !f.Fatal {
		t.Error("expected a fatal self-escalation finding")
	}
}

func TestLoad_ErrorsOnEmptyDirectory(t *testing.T) {
	if _, _, err := Load(t.TempDir()); err == nil {
		t.Fatal("an empty manifest directory must be an error, not an empty workforce")
	}
}

// ─────────────────────────────────────────────────────────────────────
// Budget parsing — 6dp, matching USDC's ERC-20 surface on Arc
// ─────────────────────────────────────────────────────────────────────

func TestParseUSDC(t *testing.T) {
	ok := map[string]int64{
		"0":        0,
		"0.10":     100_000,
		"1.00":     1_000_000,
		"25":       25_000_000,
		"0.040000": 40_000,
		".5":       500_000,
		"  1.5  ":  1_500_000,
	}
	for in, want := range ok {
		got, err := parseUSDC(in)
		if err != nil {
			t.Errorf("parseUSDC(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseUSDC(%q) = %d, want %d", in, got, want)
		}
	}

	// Rejected rather than silently truncated — a budget quietly losing precision
	// is a financial control quietly changing value.
	for _, in := range []string{"", "-1.00", "1.0000001", "abc", "1.2.3", "1e6"} {
		if _, err := parseUSDC(in); err == nil {
			t.Errorf("parseUSDC(%q) should have failed", in)
		}
	}
}

func TestLoad_ParsesBudgetIntoMicros(t *testing.T) {
	ms, _, err := Load("../../manifests")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[Role]int64{
		RoleManager:  0,
		RoleResearch: 1_000_000,
		RoleDelivery: 100_000,
		RoleFinance:  0,
	}
	for _, m := range ms {
		if got := m.BudgetMicros; got != want[m.Role] {
			t.Errorf("%s budget = %d micros, want %d", m.Role, got, want[m.Role])
		}
	}
}
