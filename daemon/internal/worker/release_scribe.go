package worker

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

// ReleaseScribeKind is the Brain worker slot for release-notes generation.
const ReleaseScribeKind = "release-scribe"

// maxReleaseCommits bounds how many commits one report summarizes. A range wider than this is
// not silently truncated: the report says so (ReleaseHistory.Truncated) so a reader never
// mistakes a capped summary for the whole range.
const maxReleaseCommits = 500

// ReleaseCommit is one committed change as the scribe reads it. Everything here is derived from
// the commit itself — nothing is authored by the worker.
type ReleaseCommit struct {
	Hash     string // full commit hash (immutable committed state)
	Subject  string // commit subject line, verbatim
	Category string // parsed from the "type(scope): …" subject prefix; "other" when absent
	PR       string // "#86" parsed from a trailing "(#NN)"; "" when absent
}

// ReleaseHistory is the measured input to a release note: the commits in a revision range of a
// committed repository. It is evidence of what merged, never an authorization to publish.
type ReleaseHistory struct {
	Repository string
	Range      string // the requested revision range, verbatim ("" means "reachable from HEAD")
	HeadRev    string // the resolved endpoint revision the notes describe
	Commits    []ReleaseCommit
	Truncated  bool // true when the range held more than maxReleaseCommits commits
}

// ReleaseHistorySource is the Release-Scribe's repository seam, mirroring BuildProgressSource: a
// git adapter supplies production history; a deterministic adapter drives the variance test.
type ReleaseHistorySource interface {
	History(ctx context.Context, repository, revisionRange string) (ReleaseHistory, error)
}

// ReleaseScribe turns committed history into a release-notes deliverable and reports it through
// Brain's Worker seam. Like Build Monitor it has no release, payment, advance, or signing
// capability: it summarizes evidence and authorizes nothing.
type ReleaseScribe struct {
	source ReleaseHistorySource
}

// NewReleaseScribe constructs the worker around its read-only history source.
func NewReleaseScribe(source ReleaseHistorySource) *ReleaseScribe {
	return &ReleaseScribe{source: source}
}

// Kind implements Worker.
func (*ReleaseScribe) Kind() string { return ReleaseScribeKind }

// Handle reads the history once and emits a progress envelope (how much was read) before the
// notes, so Brain durably sees the measurement before the derived summary — the same ordering
// Build Monitor uses. The Purchase capability is accepted by the interface and never used: the
// scribe cannot spend, and TestReleaseScribeNeverSpends proves it at runtime on top of AT-16's
// structural guarantee.
func (w *ReleaseScribe) Handle(ctx context.Context, assignment envelope.Envelope, report Report, _ Purchase) error {
	if w == nil || w.source == nil {
		return fmt.Errorf("release-scribe source is not configured")
	}
	var a Assignment
	if err := assignment.Decode(&a); err != nil {
		return err
	}
	// INJECTION BOUNDARY. a.Scope is a repository path and a.Range a revision range, both handed to
	// git as read-only arguments (never a shell). Safe for the same reason Build Monitor is: the
	// worker kind is deterministic (the LLM scoper sets scope TEXT only, workerKind fixed), so
	// model output never reaches this line as a repository or a range. GitLogSource additionally
	// validates the range shape before it touches git. If workerKind is ever made model-selectable,
	// re-audit this boundary.
	repository := strings.TrimSpace(a.Scope)
	if repository == "" {
		return fmt.Errorf("release-scribe assignment has no repository")
	}

	history, err := w.source.History(ctx, repository, strings.TrimSpace(a.Range))
	if err != nil {
		return fmt.Errorf("read history %s: %w", repository, err)
	}
	if strings.TrimSpace(history.HeadRev) == "" {
		return fmt.Errorf("history has no resolved revision")
	}

	progress, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerProgress,
		map[string]any{
			"stage":        "history-read",
			"revision":     history.HeadRev,
			"range":        history.Range,
			"commit_count": len(history.Commits),
			"truncated":    history.Truncated,
		})
	if err != nil {
		return err
	}
	if err := report(ctx, progress); err != nil {
		return err
	}

	final, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, releaseNotes(history))
	if err != nil {
		return err
	}
	return report(ctx, final)
}

// releaseNotes derives the deliverable from measured history. Every claim's evidence is the set
// of commit hashes it summarizes, so a reader can verify each line against the repository. The
// output is a pure function of the commits: a different range yields a different set of commits
// and therefore a different summary, which is what TestReleaseScribeOutputMovesWithInput asserts.
func releaseNotes(h ReleaseHistory) envelope.Deliverable {
	byCategory := map[string][]ReleaseCommit{}
	for _, c := range h.Commits {
		byCategory[c.Category] = append(byCategory[c.Category], c)
	}
	categories := make([]string, 0, len(byCategory))
	for cat := range byCategory {
		categories = append(categories, cat)
	}
	sort.Strings(categories) // stable output regardless of git's ordering within the range

	claims := make([]envelope.Claim, 0, len(categories))
	allSources := make([]string, 0, len(h.Commits))
	for _, cat := range categories {
		commits := byCategory[cat]
		prs := make([]string, 0, len(commits))
		sources := make([]string, 0, len(commits))
		for _, c := range commits {
			if c.PR != "" {
				prs = append(prs, c.PR)
			}
			src := "git:" + c.Hash
			sources = append(sources, src)
			allSources = append(allSources, src)
		}
		text := fmt.Sprintf("%s: %d change(s)", cat, len(commits))
		if len(prs) > 0 {
			text += " — " + strings.Join(prs, ", ")
		}
		claims = append(claims, envelope.Claim{Text: text, Sources: sources})
	}

	rangeLabel := h.Range
	if rangeLabel == "" {
		rangeLabel = "reachable from HEAD"
	}
	summary := fmt.Sprintf("%d change(s) across %d categor%s in %s at %s",
		len(h.Commits), len(categories), plural(len(categories), "y", "ies"), rangeLabel, shortRev(h.HeadRev))
	if h.Truncated {
		summary += fmt.Sprintf(" (capped at %d commits; the range holds more)", maxReleaseCommits)
	}

	return envelope.Deliverable{
		Title:      "Release notes",
		Summary:    summary,
		Claims:     claims,
		Sources:    allSources,
		Disclaimer: "Release notes summarize committed history; they are evidence of what merged, not authorization to publish or release.",
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func shortRev(rev string) string {
	if len(rev) > 12 {
		return rev[:12]
	}
	return rev
}

// prPattern extracts the PR number from a trailing "(#NN)" — the marker this repo's squash
// merges carry. subjectPattern splits "type(scope): summary" into its leading type.
var (
	prPattern      = regexp.MustCompile(`\(#(\d+)\)`)
	categoryPrefix = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9_-]*)(?:\([^)]*\))?:`)
)

// parseCommit derives a ReleaseCommit's category and PR from its subject alone. Unparseable
// subjects are honestly categorized "other" rather than force-fit into a bucket.
func parseCommit(hash, subject string) ReleaseCommit {
	c := ReleaseCommit{Hash: hash, Subject: subject, Category: "other"}
	if m := categoryPrefix.FindStringSubmatch(subject); m != nil {
		c.Category = strings.ToLower(m[1])
	}
	if m := prPattern.FindStringSubmatch(subject); m != nil {
		c.PR = "#" + m[1]
	}
	return c
}
