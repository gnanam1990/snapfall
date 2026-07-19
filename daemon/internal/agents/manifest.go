// Package agents loads and validates AI-employee manifests (PRD §4.1, FR-ORG-003, FR-ORG-006).
//
// Validation is deterministic code, never a model judgement. A manifest that would grant an
// agent authority the architecture forbids is rejected outright — not warned about — because
// the whole trust model rests on agents being unable to sign, borrow, or reach the network
// by default (FR-PAY-001, FR-FLT-001, SEC-007).
package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Role is one of the four bounded MVP roles. PRD §2.5 lists ">4 agent roles" as WON'T,
// so an unknown role is a scope violation, not an extension point.
type Role string

const (
	RoleManager  Role = "manager"
	RoleResearch Role = "research"
	RoleDelivery Role = "delivery"
	RoleFinance  Role = "finance"
)

var knownRoles = map[Role]bool{
	RoleManager: true, RoleResearch: true, RoleDelivery: true, RoleFinance: true,
}

// Model is the local inference endpoint for a role (PRD principle: local-first).
type Model struct {
	Provider string `yaml:"provider"`
	Name     string `yaml:"name"`
}

// Manifest mirrors daemon/manifests/*.yaml exactly.
//
// NOTE: this is a flatter schema than the illustrative example in PRD §8.2 (which nests
// permissions/finance/escalation and carries an `id`). The files on disk are authoritative
// here; see docs/OPEN-SPEC-QUESTIONS.md SPEC-05.
type Manifest struct {
	Role             Role     `yaml:"role"`
	Model            Model    `yaml:"model"`
	MemoryNamespace  string   `yaml:"memory_namespace"`
	FilesystemScope  []string `yaml:"filesystem_scope"`
	CommandAllowlist []string `yaml:"command_allowlist"`
	NetworkAllowlist []string `yaml:"network_allowlist"`
	BudgetUSDC       string   `yaml:"budget_usdc"`
	CanSignPayments  bool     `yaml:"can_sign_payments"`
	CanRequestAdv    bool     `yaml:"can_request_advance"`
	EscalatesTo      string   `yaml:"escalates_to"`
	Responsibilities []string `yaml:"responsibilities"`

	// SourcePath is where this manifest was read from, for diagnostics.
	SourcePath string `yaml:"-"`
	// BudgetMicros is BudgetUSDC parsed into atomic 6dp units, matching USDC's ERC-20 surface.
	BudgetMicros int64 `yaml:"-"`
}

// Finding is one validation result. Fatal findings block activation (FR-ORG-006);
// non-fatal ones are reported for a human to judge.
type Finding struct {
	Role    Role
	Path    string
	Fatal   bool
	Code    string
	Message string
}

func (f Finding) String() string {
	sev := "WARN "
	if f.Fatal {
		sev = "UNSAFE"
	}
	return fmt.Sprintf("%s %-24s %s: %s", sev, f.Code, f.Role, f.Message)
}

// shellBinaries are interpreters that turn a command allowlist into arbitrary execution.
// PRD §4.1 restricts Research to "no arbitrary shell"; the same reasoning applies to every role.
var shellBinaries = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"env": true, "eval": true, "exec": true, "xargs": true, "sudo": true, "ssh": true,
}

// wildcardHosts defeat deny-by-default egress (SEC-007).
var wildcardHosts = map[string]bool{
	"*": true, "0.0.0.0/0": true, "::/0": true, "any": true, "all": true,
}

// Load reads and validates every *.yaml manifest in dir.
//
// It returns the manifests that parsed, every finding, and an error if any finding is fatal
// or any file failed to parse. Callers must not activate a workforce when err != nil.
func Load(dir string) ([]Manifest, []Finding, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, nil, fmt.Errorf("globbing %s: %w", dir, err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, nil, fmt.Errorf("no manifests found in %s", dir)
	}

	var (
		manifests []Manifest
		findings  []Finding
		parseErrs []error
	)

	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("reading %s: %w", p, err))
			continue
		}
		var m Manifest
		// KnownFields catches typo'd keys that would otherwise silently grant defaults —
		// a manifest with `can_sign_payment: true` (missing the s) must not parse as false.
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		dec.KnownFields(true)
		if err := dec.Decode(&m); err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("parsing %s: %w", p, err))
			continue
		}
		m.SourcePath = p
		findings = append(findings, validate(&m)...)
		manifests = append(manifests, m)
	}

	findings = append(findings, validateSet(manifests)...)

	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Fatal != findings[j].Fatal {
			return findings[i].Fatal
		}
		return findings[i].Role < findings[j].Role
	})

	err = errors.Join(parseErrs...)
	for _, f := range findings {
		if f.Fatal {
			err = errors.Join(err, fmt.Errorf("%s", f))
		}
	}
	return manifests, findings, err
}

// validate checks one manifest in isolation.
func validate(m *Manifest) []Finding {
	var out []Finding
	add := func(fatal bool, code, format string, args ...any) {
		out = append(out, Finding{
			Role: m.Role, Path: m.SourcePath, Fatal: fatal,
			Code: code, Message: fmt.Sprintf(format, args...),
		})
	}

	// ── Architecture law. These are non-negotiable; a manifest asserting them is rejected. ──

	// FR-PAY-001 / SEC-002: agents never access keys. Signing lives in the treasury service.
	if m.CanSignPayments {
		add(true, "agent-may-sign", "can_sign_payments must be false; only the isolated treasury signer signs (FR-PAY-001)")
	}
	// FR-FLT-001 / SEC-011 / ADR-009: advances are human-authorized, treasury-only.
	if m.CanRequestAdv {
		add(true, "agent-may-borrow", "can_request_advance must be false; advances require authenticated human authorization (FR-FLT-001)")
	}

	// ── Structural validity ──

	if m.Role == "" {
		add(true, "missing-role", "role is required")
	} else if !knownRoles[m.Role] {
		add(true, "unknown-role", "not one of the four bounded MVP roles (PRD §4.1); >4 roles is out of scope")
	}
	if m.MemoryNamespace == "" {
		add(true, "missing-namespace", "memory_namespace is required for namespace isolation (FR-MEM-001)")
	}
	if m.Model.Provider == "" || m.Model.Name == "" {
		add(true, "incomplete-model", "model.provider and model.name are both required")
	}
	if m.EscalatesTo == "" {
		add(true, "missing-escalation", "escalates_to is required; every agent needs an escalation path (FR-ORG-003)")
	} else if m.EscalatesTo != "human" && !knownRoles[Role(m.EscalatesTo)] {
		add(true, "unknown-escalation", "escalates_to %q is neither \"human\" nor a known role", m.EscalatesTo)
	} else if string(m.Role) == m.EscalatesTo {
		add(true, "self-escalation", "escalates_to points at itself; escalation would loop forever")
	}

	micros, err := parseUSDC(m.BudgetUSDC)
	if err != nil {
		add(true, "bad-budget", "budget_usdc %q: %v", m.BudgetUSDC, err)
	} else {
		m.BudgetMicros = micros
	}

	// ── Unsafe permissions ──

	for _, cmd := range m.CommandAllowlist {
		base := strings.ToLower(filepath.Base(strings.Fields(cmd)[0]))
		if shellBinaries[base] {
			add(true, "shell-in-allowlist", "command_allowlist contains %q, which grants arbitrary execution (PRD §4.1)", cmd)
		}
	}
	for _, host := range m.NetworkAllowlist {
		if wildcardHosts[strings.ToLower(strings.TrimSpace(host))] {
			add(true, "wildcard-egress", "network_allowlist contains %q, defeating deny-by-default egress (SEC-007)", host)
		}
	}

	// ── Contradictions: legal, but the permissions do not add up. Human judges. ──

	if m.BudgetMicros > 0 && len(m.NetworkAllowlist) == 0 {
		add(false, "budget-without-egress", "budget of %s USDC is unusable: network_allowlist is empty, so no merchant is reachable", m.BudgetUSDC)
	}
	if len(m.CommandAllowlist) > 0 && len(m.FilesystemScope) == 0 {
		add(false, "commands-without-workspace", "command_allowlist is non-empty but filesystem_scope is empty, so commands have nowhere to read or write")
	}
	if len(m.Responsibilities) == 0 {
		add(false, "no-responsibilities", "no responsibilities declared; the role's purpose is undocumented")
	}

	return out
}

// validateSet checks properties that only hold across the whole workforce.
func validateSet(ms []Manifest) []Finding {
	var out []Finding
	seen := map[Role]string{}
	for _, m := range ms {
		if m.Role == "" {
			continue
		}
		if prev, dup := seen[m.Role]; dup {
			out = append(out, Finding{
				Role: m.Role, Path: m.SourcePath, Fatal: true, Code: "duplicate-role",
				Message: fmt.Sprintf("role already defined in %s; one manifest per role", prev),
			})
			continue
		}
		seen[m.Role] = m.SourcePath
	}
	return out
}

// parseUSDC converts a decimal USDC string into atomic 6dp units.
//
// Arc exposes USDC with 18 decimals natively and 6 through the ERC-20 surface; all Snapfall
// accounting uses the 6dp surface, so "1.00" is 1_000_000. Rejects anything finer than 6dp
// rather than silently truncating a budget.
func parseUSDC(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty")
	}
	if strings.HasPrefix(s, "-") {
		return 0, errors.New("must not be negative")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if len(frac) > 6 {
		return 0, fmt.Errorf("more precision than USDC's 6 decimals: %q", s)
	}
	frac += strings.Repeat("0", 6-len(frac))

	var micros int64
	for _, part := range []string{whole, frac} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, fmt.Errorf("not a decimal number: %q", s)
			}
		}
	}
	for _, r := range whole {
		micros = micros*10 + int64(r-'0')
		if micros > (1<<62)/1_000_000 {
			return 0, errors.New("implausibly large")
		}
	}
	micros *= 1_000_000

	var fracVal int64
	for _, r := range frac {
		fracVal = fracVal*10 + int64(r-'0')
	}
	return micros + fracVal, nil
}
