package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const maxChecklistBytes = 1 << 20

// GitChecklistSource measures milestone artifacts from the committed HEAD tree. It
// deliberately ignores the working tree so the reported revision and evidence refer
// to the same immutable repository state.
type GitChecklistSource struct {
	ChecklistPath string
}

type milestoneChecklist struct {
	Checks []milestoneCheck `json:"checks"`
}

type milestoneCheck struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type gitTreeEntry struct {
	mode string
	kind string
	hash string
}

func (s GitChecklistSource) Snapshot(ctx context.Context, repository string) (BuildSnapshot, error) {
	repo, err := canonicalRepository(ctx, repository)
	if err != nil {
		return BuildSnapshot{}, err
	}
	revision, err := gitOutput(ctx, repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return BuildSnapshot{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	revision = strings.TrimSpace(revision)
	if !isHexRevision(revision) {
		return BuildSnapshot{}, fmt.Errorf("HEAD is not a commit hash")
	}

	checklistPath := strings.TrimSpace(s.ChecklistPath)
	if checklistPath == "" {
		checklistPath = ".snapfall/milestone.json"
	}
	checklistPath, err = confinedGitPath(checklistPath)
	if err != nil {
		return BuildSnapshot{}, fmt.Errorf("checklist path: %w", err)
	}
	entry, found, err := treeEntry(ctx, repo, revision, checklistPath)
	if err != nil {
		return BuildSnapshot{}, err
	}
	if !found {
		return BuildSnapshot{}, fmt.Errorf("checklist %s is not committed at HEAD", checklistPath)
	}
	if entry.mode == "120000" {
		return BuildSnapshot{}, fmt.Errorf("checklist %s is a symlink", checklistPath)
	}
	if entry.kind != "blob" {
		return BuildSnapshot{}, fmt.Errorf("checklist %s is not a regular file at HEAD", checklistPath)
	}
	raw, err := gitBlob(ctx, repo, entry.hash)
	if err != nil {
		return BuildSnapshot{}, fmt.Errorf("read checklist %s: %w", checklistPath, err)
	}

	var checklist milestoneChecklist
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checklist); err != nil {
		return BuildSnapshot{}, fmt.Errorf("decode checklist %s: %w", checklistPath, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return BuildSnapshot{}, fmt.Errorf("decode checklist %s: trailing JSON", checklistPath)
	}
	if len(checklist.Checks) == 0 {
		return BuildSnapshot{}, fmt.Errorf("checklist %s has no checks", checklistPath)
	}

	snapshot := BuildSnapshot{Revision: revision}
	seen := make(map[string]bool, len(checklist.Checks))
	for i, check := range checklist.Checks {
		if err := ctx.Err(); err != nil {
			return BuildSnapshot{}, err
		}
		name := strings.TrimSpace(check.Name)
		if name == "" {
			return BuildSnapshot{}, fmt.Errorf("check %d has no name", i)
		}
		if seen[name] {
			return BuildSnapshot{}, fmt.Errorf("duplicate check %q", name)
		}
		seen[name] = true
		artifactPath, err := confinedGitPath(check.Path)
		if err != nil {
			return BuildSnapshot{}, fmt.Errorf("check %q path: %w", name, err)
		}
		entry, found, err := treeEntry(ctx, repo, revision, artifactPath)
		if err != nil {
			return BuildSnapshot{}, fmt.Errorf("check %q: %w", name, err)
		}
		if found && entry.mode == "120000" {
			return BuildSnapshot{}, fmt.Errorf("check %q path %s is a symlink", name, artifactPath)
		}
		if found {
			snapshot.Completed = append(snapshot.Completed, name)
		} else {
			snapshot.Pending = append(snapshot.Pending, name)
		}
	}
	snapshot.CompletionPct = len(snapshot.Completed) * 100 / len(checklist.Checks)
	return snapshot, nil
}

func canonicalRepository(ctx context.Context, repository string) (string, error) {
	assigned, err := filepath.Abs(strings.TrimSpace(repository))
	if err != nil {
		return "", fmt.Errorf("resolve repository: %w", err)
	}
	assigned, err = filepath.EvalSymlinks(assigned)
	if err != nil {
		return "", fmt.Errorf("resolve repository: %w", err)
	}
	info, err := os.Stat(assigned)
	if err != nil {
		return "", fmt.Errorf("stat repository: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("repository is not a directory")
	}
	root, err := gitOutput(ctx, assigned, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(root))
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	if root != assigned {
		return "", fmt.Errorf("assigned repository must be its Git worktree root")
	}
	return root, nil
}

func confinedGitPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("absolute path is not allowed")
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes repository")
	}
	return filepath.ToSlash(clean), nil
}

func treeEntry(ctx context.Context, repo, revision, name string) (gitTreeEntry, bool, error) {
	out, err := gitBytes(ctx, repo, "ls-tree", "-z", revision, "--", name)
	if err != nil {
		return gitTreeEntry{}, false, fmt.Errorf("inspect committed path %s: %w", name, err)
	}
	if len(out) == 0 {
		return gitTreeEntry{}, false, nil
	}
	out = bytes.TrimSuffix(out, []byte{0})
	header, listed, ok := bytes.Cut(out, []byte{'\t'})
	if !ok || string(listed) != name {
		return gitTreeEntry{}, false, fmt.Errorf("unexpected Git tree entry for %s", name)
	}
	fields := strings.Fields(string(header))
	if len(fields) != 3 {
		return gitTreeEntry{}, false, fmt.Errorf("malformed Git tree entry for %s", name)
	}
	return gitTreeEntry{mode: fields[0], kind: fields[1], hash: fields[2]}, true, nil
}

func gitBlob(ctx context.Context, repo, hash string) ([]byte, error) {
	sizeText, err := gitOutput(ctx, repo, "cat-file", "-s", hash)
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid blob size: %w", err)
	}
	if size > maxChecklistBytes {
		return nil, fmt.Errorf("checklist exceeds %d bytes", maxChecklistBytes)
	}
	return gitBytes(ctx, repo, "cat-file", "blob", hash)
}

func gitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	out, err := gitBytes(ctx, repo, args...)
	return string(out), err
}

func gitBytes(ctx context.Context, repo string, args ...string) ([]byte, error) {
	gitArgs := append([]string{"--literal-pathspecs", "-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", gitArgs...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_OPTIONAL_LOCKS=0",
	)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func isHexRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}
