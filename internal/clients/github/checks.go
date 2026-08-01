package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/0x63616c/software-factory/internal/work"
	gh "github.com/google/go-github/v78/github"
)

// ChecksForRef returns every check run GitHub has recorded against ref — a
// branch name, in this service's only caller — as one snapshot.
//
// It takes no view on whether the checks it returns have concluded or
// passed: Activities.ObserveCI is what polls this repeatedly, waiting for a
// concluded result or its own bound, and reduces the snapshot into
// concluded/green/red for the implement/review loop's progress-detection
// rules. The client does not poll: it obtains one check-run snapshot and the
// annotations needed to identify its failed runs. Polling belongs to the
// activity, which owns the wait and the bound on it.
func (c *Client) ChecksForRef(ctx context.Context, ref string) ([]work.CheckRun, error) {
	return c.checksForRef(ctx, ref, nil, false)
}

// ChecksForCommit returns one check-run snapshot for exactly commitSHA.
// GitHub accepts a SHA in the same reference position as a branch; this
// separate method makes the immutable target-path contract explicit. Only
// required checks are returned or enriched with failure details.
func (c *Client) ChecksForCommit(ctx context.Context, commitSHA string, requiredChecks []string) ([]work.CheckRun, error) {
	if strings.TrimSpace(commitSHA) == "" {
		return nil, fmt.Errorf("listing check runs for an empty commit SHA: %w", work.ErrPermanent)
	}
	required := make(map[string]struct{}, len(requiredChecks))
	for _, name := range requiredChecks {
		required[name] = struct{}{}
	}
	return c.checksForRef(ctx, commitSHA, required, true)
}

func (c *Client) checksForRef(
	ctx context.Context,
	ref string,
	includedChecks map[string]struct{},
	includeFailureEvidence bool,
) ([]work.CheckRun, error) {
	op := fmt.Sprintf("listing check runs for %s", ref)

	opts := &gh.ListCheckRunsOptions{ListOptions: gh.ListOptions{PerPage: perPage}}

	var runs []work.CheckRun
	for {
		result, resp, err := c.api.Checks.ListCheckRunsForRef(ctx, c.owner, c.repo, ref, opts)
		if err != nil {
			return nil, classify(ctx, op, err)
		}
		for _, run := range result.CheckRuns {
			if includedChecks != nil {
				if _, included := includedChecks[run.GetName()]; !included {
					continue
				}
			}
			check := work.CheckRun{
				Name:       run.GetName(),
				Completed:  run.GetStatus() == "completed",
				Conclusion: run.GetConclusion(),
			}
			if check.Completed && !check.Green() && !check.Superseded() {
				if includeFailureEvidence {
					check.FailureEvidence = boundedCheckFailureEvidence(run)
				}
				fingerprint, err := c.checkFailureFingerprint(ctx, run)
				if err != nil {
					return nil, err
				}
				check.FailureFingerprint = fingerprint
			}
			runs = append(runs, check)
		}
		if resp.NextPage == 0 {
			return runs, nil
		}
		opts.Page = resp.NextPage
	}
}

// checkFailureEvidenceMaxBytes bounds untrusted check output retained in a
// target Run handoff. Fingerprints keep their separate legacy identity role.
const checkFailureEvidenceMaxBytes = 2 << 10

func boundedCheckFailureEvidence(run *gh.CheckRun) string {
	output := run.GetOutput()
	parts := make([]string, 0, 3)
	for _, value := range []string{output.GetTitle(), output.GetSummary(), output.GetText()} {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return truncateUTF8(strings.Join(parts, "\n"), checkFailureEvidenceMaxBytes)
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && (value[cut]&0xc0) == 0x80 {
		cut--
	}
	return value[:cut]
}

// checkFailureFingerprint reduces one failed check's output to a stable,
// opaque identity. The workflow persists only the digest, not CI text or
// annotations, which may be large and attacker-controlled.
func (c *Client) checkFailureFingerprint(ctx context.Context, run *gh.CheckRun) (string, error) {
	const op = "reading failed check details"

	if run.GetID() == 0 {
		return "", fmt.Errorf("%s: github returned failed check %q with no id", op, run.GetName())
	}

	annotations, err := c.checkRunAnnotations(ctx, run.GetID())
	if err != nil {
		return "", err
	}

	detail := checkFailureDetail{
		Conclusion:  run.GetConclusion(),
		Title:       run.GetOutput().GetTitle(),
		Summary:     run.GetOutput().GetSummary(),
		Text:        run.GetOutput().GetText(),
		Annotations: make([]checkAnnotationDetail, 0, len(annotations)),
	}
	for _, annotation := range annotations {
		candidate := checkAnnotationDetail{
			Path:            annotation.GetPath(),
			StartLine:       annotation.GetStartLine(),
			EndLine:         annotation.GetEndLine(),
			StartColumn:     annotation.GetStartColumn(),
			EndColumn:       annotation.GetEndColumn(),
			AnnotationLevel: annotation.GetAnnotationLevel(),
			Title:           annotation.GetTitle(),
			Message:         annotation.GetMessage(),
			RawDetails:      annotation.GetRawDetails(),
		}
		if !identifyingAnnotation(candidate) {
			continue
		}
		detail.Annotations = append(detail.Annotations, candidate)
	}
	if !detail.hasEvidence() {
		// GitHub Actions' generic exit-code annotation says a job failed but
		// not which assertion or test failed. Treating it as an identity would
		// turn a different failure in the same job into a false stagnation.
		return c.actionsLogFailureFingerprint(ctx, run)
	}
	sort.Slice(detail.Annotations, func(i, j int) bool {
		return annotationKey(detail.Annotations[i]) < annotationKey(detail.Annotations[j])
	})

	encoded, err := json.Marshal(detail)
	if err != nil {
		return "", fmt.Errorf("%s: serializing check %q details: %w", op, run.GetName(), err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// jobLogMaxBytes bounds the optional Actions log download. A test name is
// normally near the end, but an unbounded log is not a safe CI observation.
const jobLogMaxBytes = 2 << 20

// actionsLogFailureFingerprint obtains an identity from the job log when a
// GitHub Actions check's Checks API payload is only its generic exit-code
// message. Failing Go tests are the strongest identity; a job that is not a
// Go test run — a generator drift gate, a lint step — is identified by the
// error lines the runner logged instead. Actions-log access is an optional
// enrichment: an unavailable log, or a log carrying nothing beyond the
// generic exit-code line, leaves the check unidentified so rule 1 cannot
// mistake it for a proven repeat.
func (c *Client) actionsLogFailureFingerprint(ctx context.Context, run *gh.CheckRun) (string, error) {
	jobID, ok := actionsJobID(run.GetDetailsURL())
	if !ok {
		return "", nil
	}

	logURL, _, err := c.api.Actions.GetWorkflowJobLogs(ctx, c.owner, c.repo, jobID, 0)
	if err != nil || logURL == nil {
		return "", nil
	}
	log, err := c.downloadJobLog(ctx, logURL)
	if err != nil {
		return "", nil
	}

	if failedTests := failedGoTests(log); len(failedTests) > 0 {
		encoded, err := json.Marshal(struct {
			FailedTests []string `json:"failed_tests"`
		}{FailedTests: failedTests})
		if err != nil {
			return "", fmt.Errorf("serializing failed Go tests for check %q: %w", run.GetName(), err)
		}
		digest := sha256.Sum256(encoded)
		return hex.EncodeToString(digest[:]), nil
	}

	errorLines := loggedErrorLines(log)
	if len(errorLines) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(struct {
		ErrorLines []string `json:"error_lines"`
	}{ErrorLines: errorLines})
	if err != nil {
		return "", fmt.Errorf("serializing logged errors for check %q: %w", run.GetName(), err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// loggedErrorLines returns the distinct error messages a GitHub Actions job
// logged through its `##[error]` workflow command, sorted so log ordering
// cannot move the identity.
//
// The runner's generic exit-code line and its run-cancellation lines are
// dropped: both appear identically whatever failed, so a job whose log holds
// nothing else stays deliberately unidentified rather than acquiring a
// change-independent identity.
func loggedErrorLines(log string) []string {
	const marker = "##[error]"

	seen := make(map[string]struct{})
	for _, line := range strings.Split(log, "\n") {
		at := strings.Index(line, marker)
		if at < 0 {
			continue
		}
		message := strings.TrimSpace(line[at+len(marker):])
		if message == "" || genericExitCodeMessage(message) || cancellationMessage(message) {
			continue
		}
		seen[message] = struct{}{}
	}

	messages := make([]string, 0, len(seen))
	for message := range seen {
		messages = append(messages, message)
	}
	sort.Strings(messages)
	return messages
}

// cancellationMessage reports the runner's own messages for a run GitHub
// cancelled, most often because a newer push superseded it.
func cancellationMessage(message string) bool {
	return message == "The operation was canceled." ||
		strings.HasPrefix(message, "Canceling since a higher priority waiting request")
}

func actionsJobID(detailsURL string) (int64, bool) {
	parsed, err := url.Parse(detailsURL)
	if err != nil {
		return 0, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[len(parts)-2] != "job" {
		return 0, false
	}
	jobID, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	return jobID, err == nil && jobID > 0
}

func (c *Client) downloadJobLog(ctx context.Context, logURL *url.URL) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("building Actions job-log request: %w", err)
	}
	resp, err := c.downloads.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading Actions job log: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if err := resp.Body.Close(); err != nil {
			return "", fmt.Errorf("closing failed Actions job log response: %w", err)
		}
		return "", fmt.Errorf("downloading Actions job log: got HTTP %d", resp.StatusCode)
	}
	bytes, err := io.ReadAll(io.LimitReader(resp.Body, jobLogMaxBytes))
	if err != nil {
		if closeErr := resp.Body.Close(); closeErr != nil {
			return "", fmt.Errorf("reading Actions job log: %w; closing response: %w", err, closeErr)
		}
		return "", fmt.Errorf("reading Actions job log: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return "", fmt.Errorf("closing Actions job log response: %w", err)
	}
	return string(bytes), nil
}

// failedGoTests returns one identity per failing test, qualified by its
// package. `go test` prints a bare test name on each "--- FAIL:" line, so two
// different packages' same-named tests (e.g. TestValidate in two packages)
// would otherwise fingerprint identically and hide a real change in what's
// failing. Qualification comes from the "FAIL\t<package>\t<elapsed>" summary
// line `go test` emits once a package's run finishes; any failures logged
// before a summary line is seen (a truncated log) fall back to the bare name
// rather than being dropped.
func failedGoTests(log string) []string {
	const marker = "--- FAIL: "

	seen := make(map[string]struct{})
	var pending []string
	for _, line := range strings.Split(log, "\n") {
		if at := strings.Index(line, marker); at >= 0 {
			rest := line[at+len(marker):]
			name, _, _ := strings.Cut(rest, " ")
			if name != "" {
				pending = append(pending, name)
			}
			continue
		}
		pkg, ok := goTestPackageSummary(line)
		if !ok || len(pending) == 0 {
			continue
		}
		for _, name := range pending {
			seen[pkg+"."+name] = struct{}{}
		}
		pending = pending[:0]
	}
	for _, name := range pending {
		seen[name] = struct{}{}
	}

	tests := make([]string, 0, len(seen))
	for name := range seen {
		tests = append(tests, name)
	}
	sort.Strings(tests)
	return tests
}

// goTestPackageSummary reports the package from a "FAIL\t<package>\t<elapsed>"
// or "ok  \t<package>\t<elapsed>" line, tolerating a leading Actions log
// timestamp. It ignores the bare "--- FAIL: <test>" lines that precede it,
// since those never carry "FAIL"/"ok" as a standalone token.
func goTestPackageSummary(line string) (string, bool) {
	fields := strings.Fields(line)
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] != "FAIL" && fields[i] != "ok" {
			continue
		}
		pkg := fields[i+1]
		if pkg == "" || strings.HasPrefix(pkg, "[") {
			continue
		}
		return pkg, true
	}
	return "", false
}

// checkRunAnnotations returns every annotation for a failed check or no
// snapshot at all. A partial failure fingerprint could falsely look like a
// changed failure on the next turn.
func (c *Client) checkRunAnnotations(ctx context.Context, checkRunID int64) ([]*gh.CheckRunAnnotation, error) {
	op := fmt.Sprintf("listing annotations for check run %d", checkRunID)
	opts := &gh.ListOptions{PerPage: perPage}

	var annotations []*gh.CheckRunAnnotation
	for {
		page, resp, err := c.api.Checks.ListCheckRunAnnotations(ctx, c.owner, c.repo, checkRunID, opts)
		if err != nil {
			return nil, classify(ctx, op, err)
		}
		annotations = append(annotations, page...)
		if resp.NextPage == 0 {
			return annotations, nil
		}
		opts.Page = resp.NextPage
	}
}

type checkFailureDetail struct {
	Conclusion  string                  `json:"conclusion"`
	Title       string                  `json:"title"`
	Summary     string                  `json:"summary"`
	Text        string                  `json:"text"`
	Annotations []checkAnnotationDetail `json:"annotations"`
}

func (d checkFailureDetail) hasEvidence() bool {
	return d.Title != "" || d.Summary != "" || d.Text != "" || len(d.Annotations) > 0
}

type checkAnnotationDetail struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	StartColumn     int    `json:"start_column"`
	EndColumn       int    `json:"end_column"`
	AnnotationLevel string `json:"annotation_level"`
	Title           string `json:"title"`
	Message         string `json:"message"`
	RawDetails      string `json:"raw_details"`
}

// identifyingAnnotation reports whether an annotation says anything about
// what failed on this turn specifically.
//
// Two kinds are excluded. Anything that is not failure-level — the runner's
// standing Node-version deprecation warning is on every job in this
// repository — is identical on every turn of every check, so keeping it
// would give a check a stable identity that no code change can ever move,
// and rule 1 would read the second red turn as stagnation whatever the agent
// did. The runner's stock exit-code annotation is failure-level but says
// only that some step exited non-zero, not which one or why.
func identifyingAnnotation(annotation checkAnnotationDetail) bool {
	if annotation.AnnotationLevel != "failure" {
		return false
	}
	return annotation.Title != "" || annotation.RawDetails != "" ||
		!genericExitCodeMessage(annotation.Message)
}

// genericExitCodeMessage reports the runner's stock "a step exited non-zero"
// message, whatever the code.
func genericExitCodeMessage(message string) bool {
	const prefix = "Process completed with exit code "
	code, ok := strings.CutPrefix(message, prefix)
	if !ok {
		return false
	}
	code, ok = strings.CutSuffix(code, ".")
	if !ok || code == "" {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func annotationKey(annotation checkAnnotationDetail) string {
	return fmt.Sprintf("%q:%d:%d:%d:%d:%q:%q:%q:%q",
		annotation.Path,
		annotation.StartLine,
		annotation.EndLine,
		annotation.StartColumn,
		annotation.EndColumn,
		annotation.AnnotationLevel,
		annotation.Title,
		annotation.Message,
		annotation.RawDetails,
	)
}
