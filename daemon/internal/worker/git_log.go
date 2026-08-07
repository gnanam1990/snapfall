package worker

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// GitLogSource reads committed history from a repository's Git log over a revision range. Like
// GitChecklistSource it reads committed state only (the log, not the working tree) and shells to
// git read-only — the same confined os/exec seam, no shell.
type GitLogSource struct{}

// safeRevisionRange constrains what may be handed to git as a range. It forbids a leading dash
// (so a range can never be read as a git flag), whitespace, and shell metacharacters, and allows
// a single revision or a `from..to` / `from...to` range of the usual git revision characters.
// The range never reaches a shell — this is defense in depth against a flag-injection endpoint.
var safeRevisionRange = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/~^@-]*(\.\.\.?[A-Za-z0-9][A-Za-z0-9._/~^@-]*)?$`)

// History resolves the range, reads the commits in it, and derives their category/PR from the
// subject line. An unresolvable or malformed range is an error, never a silent empty result.
func (GitLogSource) History(ctx context.Context, repository, revisionRange string) (ReleaseHistory, error) {
	repo, err := canonicalRepository(ctx, repository)
	if err != nil {
		return ReleaseHistory{}, err
	}

	logArg := "HEAD"
	if revisionRange != "" {
		if !safeRevisionRange.MatchString(revisionRange) {
			return ReleaseHistory{}, fmt.Errorf("revision range %q is not a valid git range", revisionRange)
		}
		logArg = revisionRange
	}

	// The endpoint the notes describe: the right side of a `a..b` range, the range itself when it
	// is a single revision, or HEAD when unspecified.
	endpoint := logArg
	if i := strings.LastIndex(logArg, ".."); i >= 0 {
		endpoint = logArg[i+2:]
	}
	if endpoint == "" {
		endpoint = "HEAD"
	}
	head, err := gitOutput(ctx, repo, "rev-parse", "--verify", endpoint+"^{commit}")
	if err != nil {
		return ReleaseHistory{}, fmt.Errorf("resolve range endpoint %q: %w", endpoint, err)
	}
	head = strings.TrimSpace(head)
	if !isHexRevision(head) {
		return ReleaseHistory{}, fmt.Errorf("range endpoint did not resolve to a commit")
	}

	// %H<US>%s<RS> per commit: full hash and subject, separated by control bytes a subject cannot
	// contain, so parsing never depends on the free-text content of a message. One extra commit is
	// requested so a range wider than the cap is detected rather than silently trimmed.
	raw, err := gitOutput(ctx, repo, "log", logArg, "--no-color", "--no-merges",
		fmt.Sprintf("-n%d", maxReleaseCommits+1), "--pretty=format:%H%x1f%s%x1e")
	if err != nil {
		return ReleaseHistory{}, fmt.Errorf("read log %q: %w", logArg, err)
	}

	history := ReleaseHistory{Repository: repo, Range: revisionRange, HeadRev: head}
	records := strings.Split(strings.Trim(raw, "\x1e\n"), "\x1e")
	for _, rec := range records {
		rec = strings.Trim(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x1f", 2)
		if len(parts) != 2 {
			return ReleaseHistory{}, fmt.Errorf("malformed log record")
		}
		hash := strings.TrimSpace(parts[0])
		if !isHexRevision(hash) {
			return ReleaseHistory{}, fmt.Errorf("log record has no commit hash")
		}
		if len(history.Commits) == maxReleaseCommits {
			history.Truncated = true
			break
		}
		history.Commits = append(history.Commits, parseCommit(hash, strings.TrimSpace(parts[1])))
	}
	return history, nil
}
