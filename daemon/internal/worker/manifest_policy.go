package worker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// defaultManifestDir is the committed directory of agent manifests this tool lints by default.
const defaultManifestDir = "daemon/manifests"

// ManifestPolicySource lints committed agent manifests against Snapfall's least-privilege policy.
// It reads the committed tree only (never the working tree), the same discipline Build Monitor
// uses, so a finding refers to the same immutable revision it is reported against.
//
// It deliberately does NOT import internal/agents (AT-16 keeps the worker package to envelope +
// stdlib + third-party). It parses only the posture fields it judges, so a manifest-schema change
// elsewhere cannot silently widen what this tool trusts.
type ManifestPolicySource struct {
	// Dir is the repo-relative manifest directory; empty means daemon/manifests.
	Dir string
}

// scannedManifest is the minimal posture view this lint judges — a subset of agents.Manifest,
// duplicated on purpose so the rule set is explicit about exactly what it reads.
type scannedManifest struct {
	Role             string   `yaml:"role"`
	CommandAllowlist []string `yaml:"command_allowlist"`
	CanSignPayments  bool     `yaml:"can_sign_payments"`
	CanRequestAdv    bool     `yaml:"can_request_advance"`
	EscalatesTo      string   `yaml:"escalates_to"`
}

// Scan resolves HEAD, lists the committed manifest files, and evaluates every rule against each.
func (s ManifestPolicySource) Scan(ctx context.Context, root string) (ComplianceScan, error) {
	repo, err := canonicalRepository(ctx, root)
	if err != nil {
		return ComplianceScan{}, err
	}
	revision, err := gitOutput(ctx, repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return ComplianceScan{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	revision = strings.TrimSpace(revision)
	if !isHexRevision(revision) {
		return ComplianceScan{}, fmt.Errorf("HEAD is not a commit hash")
	}

	dir := strings.TrimSpace(s.Dir)
	if dir == "" {
		dir = defaultManifestDir
	}
	if _, err := confinedGitPath(dir); err != nil {
		return ComplianceScan{}, fmt.Errorf("manifest dir: %w", err)
	}

	// `git ls-tree -r <rev> -- <dir>` lists the committed files under dir as
	// "<mode> <type> <hash>\t<path>". -r recurses so directory entries do not stand in for their
	// files; reading the tree (not the working directory) is what keeps the scan pinned to
	// `revision`.
	listing, err := gitOutput(ctx, repo, "ls-tree", "-r", revision, "--", dir)
	if err != nil {
		return ComplianceScan{}, fmt.Errorf("list %s: %w", dir, err)
	}

	scan := ComplianceScan{Root: repo, Revision: revision, Dir: dir}
	for _, line := range strings.Split(strings.TrimSpace(listing), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			return ComplianceScan{}, fmt.Errorf("malformed ls-tree line")
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return ComplianceScan{}, fmt.Errorf("malformed ls-tree entry %q", meta)
		}
		mode, kind, hash := fields[0], fields[1], fields[2]
		if kind != "blob" {
			continue // only files are manifests
		}
		if mode == "120000" {
			return ComplianceScan{}, fmt.Errorf("manifest %s is a symlink", path)
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			continue
		}
		raw, err := gitBlob(ctx, repo, hash)
		if err != nil {
			return ComplianceScan{}, fmt.Errorf("read %s: %w", path, err)
		}
		var m scannedManifest
		if err := yaml.Unmarshal(raw, &m); err != nil {
			return ComplianceScan{}, fmt.Errorf("parse %s: %w", path, err)
		}
		scan.Files++
		scan.Findings = append(scan.Findings, evaluateManifest(path, m)...)
	}
	if scan.Files == 0 {
		return ComplianceScan{}, fmt.Errorf("no manifests found under %s at HEAD", dir)
	}
	// Stable order regardless of ls-tree's: file, then rule.
	sort.SliceStable(scan.Findings, func(i, j int) bool {
		if scan.Findings[i].File != scan.Findings[j].File {
			return scan.Findings[i].File < scan.Findings[j].File
		}
		return scan.Findings[i].Rule < scan.Findings[j].Rule
	})
	return scan, nil
}

// evaluateManifest applies every least-privilege rule to one manifest. Each rule maps to a real
// Snapfall invariant the codebase already enforces elsewhere, so these are policy, not opinion:
//   - no-signing-authority   FR-PAY-001: agents never hold keys (agents.Manifest, manifest_test)
//   - no-borrowing-authority AT-15: an agent-originated advance is rejected
//   - no-arbitrary-shell     no role may run arbitrary commands (PRD §4.1)
//   - escalation-path        FR-ORG-003: every agent needs an escalation path
func evaluateManifest(path string, m scannedManifest) []PolicyFinding {
	role := m.Role
	if role == "" {
		role = "(no role)"
	}
	return []PolicyFinding{
		{
			Rule: "no-signing-authority", File: path, Subject: role,
			Violation: m.CanSignPayments,
			Evidence:  fmt.Sprintf("can_sign_payments: %t", m.CanSignPayments),
		},
		{
			Rule: "no-borrowing-authority", File: path, Subject: role,
			Violation: m.CanRequestAdv,
			Evidence:  fmt.Sprintf("can_request_advance: %t", m.CanRequestAdv),
		},
		{
			Rule: "no-arbitrary-shell", File: path, Subject: role,
			Violation: len(m.CommandAllowlist) > 0,
			Evidence:  fmt.Sprintf("command_allowlist: %d entr(y/ies)", len(m.CommandAllowlist)),
		},
		{
			Rule: "escalation-path", File: path, Subject: role,
			Violation: strings.TrimSpace(m.EscalatesTo) == "",
			Evidence:  fmt.Sprintf("escalates_to: %q", m.EscalatesTo),
		},
	}
}
