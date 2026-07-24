package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// GitChecklistSource measures objective artifact existence at one Git revision. The
// repository owns a .snapfall/milestone.json checklist; this adapter reads only paths
// inside that repository and never executes repository code.
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

// Snapshot implements BuildProgressSource.
func (s GitChecklistSource) Snapshot(ctx context.Context, repository string) (BuildSnapshot, error) {
	repo, err := filepath.Abs(repository)
	if err != nil {
		return BuildSnapshot{}, err
	}
	info, err := os.Stat(repo)
	if err != nil {
		return BuildSnapshot{}, err
	}
	if !info.IsDir() {
		return BuildSnapshot{}, fmt.Errorf("repository %s is not a directory", repo)
	}
	revision, err := gitRevision(repo)
	if err != nil {
		return BuildSnapshot{}, err
	}

	checklistName := s.ChecklistPath
	if checklistName == "" {
		checklistName = filepath.Join(".snapfall", "milestone.json")
	}
	checklistPath, err := confinedPath(repo, checklistName)
	if err != nil {
		return BuildSnapshot{}, fmt.Errorf("checklist: %w", err)
	}
	f, err := os.Open(checklistPath)
	if err != nil {
		return BuildSnapshot{}, fmt.Errorf("open checklist: %w", err)
	}
	defer f.Close()
	var checklist milestoneChecklist
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&checklist); err != nil {
		return BuildSnapshot{}, fmt.Errorf("decode checklist: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return BuildSnapshot{}, fmt.Errorf("decode checklist: trailing JSON")
	}
	if len(checklist.Checks) == 0 {
		return BuildSnapshot{}, fmt.Errorf("checklist has no checks")
	}

	snapshot := BuildSnapshot{Revision: revision}
	seen := make(map[string]bool, len(checklist.Checks))
	for _, check := range checklist.Checks {
		if err := ctx.Err(); err != nil {
			return BuildSnapshot{}, err
		}
		name := strings.TrimSpace(check.Name)
		if name == "" {
			return BuildSnapshot{}, fmt.Errorf("check has no name")
		}
		if seen[name] {
			return BuildSnapshot{}, fmt.Errorf("duplicate check %q", name)
		}
		seen[name] = true
		artifact, err := confinedPath(repo, check.Path)
		if err != nil {
			return BuildSnapshot{}, fmt.Errorf("check %q: %w", name, err)
		}
		if _, err := os.Stat(artifact); err == nil {
			snapshot.Completed = append(snapshot.Completed, name)
		} else if os.IsNotExist(err) {
			snapshot.Pending = append(snapshot.Pending, name)
		} else {
			return BuildSnapshot{}, fmt.Errorf("check %q: %w", name, err)
		}
	}
	snapshot.CompletionPct = len(snapshot.Completed) * 100 / len(checklist.Checks)
	return snapshot, nil
}

func confinedPath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("path %q must be repository-relative", relative)
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes or names no repository artifact", relative)
	}
	return filepath.Join(root, clean), nil
}

func gitRevision(repo string) (string, error) {
	gitDir := filepath.Join(repo, ".git")
	if raw, err := os.ReadFile(gitDir); err == nil {
		line := strings.TrimSpace(string(raw))
		const prefix = "gitdir:"
		if !strings.HasPrefix(line, prefix) {
			return "", fmt.Errorf("%s is not a Git directory pointer", gitDir)
		}
		gitDir = strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(repo, gitDir)
		}
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", fmt.Errorf("read Git HEAD: %w", err)
	}
	value := strings.TrimSpace(string(head))
	if strings.HasPrefix(value, "ref: ") {
		ref := strings.TrimSpace(strings.TrimPrefix(value, "ref: "))
		refPath, err := confinedPath(gitDir, ref)
		if err != nil {
			return "", fmt.Errorf("Git HEAD ref: %w", err)
		}
		raw, err := os.ReadFile(refPath)
		if err == nil {
			value = strings.TrimSpace(string(raw))
		} else if os.IsNotExist(err) {
			value, err = packedGitRef(gitDir, ref)
			if err != nil {
				return "", err
			}
		} else {
			return "", fmt.Errorf("read Git HEAD ref: %w", err)
		}
	}
	if !isHexRevision(value) {
		return "", fmt.Errorf("Git HEAD %q is not a full revision", value)
	}
	return strings.ToLower(value), nil
}

func packedGitRef(gitDir, ref string) (string, error) {
	f, err := os.Open(filepath.Join(gitDir, "packed-refs"))
	if err != nil {
		return "", fmt.Errorf("read Git HEAD ref: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(io.LimitReader(f, 1<<20))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == ref {
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read packed Git refs: %w", err)
	}
	return "", fmt.Errorf("read Git HEAD ref: %s is absent from loose and packed refs", ref)
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
