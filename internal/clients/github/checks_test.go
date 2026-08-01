package github

import (
	"net/http"
	"strings"
	"testing"
)

func TestChecksForCommitUsesTheExactCommitSHAAndBoundsFailureEvidence(t *testing.T) {
	t.Parallel()

	const sha = "0a1b2c3d4e5f"
	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+sha+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{{
			"id": 91, "name": "test", "status": "completed", "conclusion": "failure",
			"output": map[string]any{"summary": strings.Repeat("x", checkFailureEvidenceMaxBytes+100)},
		}}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{})
	})
	c, _ := s.client(t)

	checks, err := c.ChecksForCommit(t.Context(), sha, []string{"test"})
	if err != nil {
		t.Fatalf("ChecksForCommit: %v", err)
	}
	if len(checks) != 1 || len(checks[0].FailureEvidence) != checkFailureEvidenceMaxBytes {
		t.Fatalf("checks = %+v, want one check with %d-byte evidence", checks, checkFailureEvidenceMaxBytes)
	}
	if s.count("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+sha+"/check-runs") != 1 {
		t.Fatalf("did not query the exact commit SHA: %s", s)
	}
}

func TestChecksForCommitIgnoresUnrelatedNonGreenChecksBeforeReadingTheirDetails(t *testing.T) {
	t.Parallel()

	for _, conclusion := range []string{"failure", "cancelled"} {
		t.Run(conclusion, func(t *testing.T) {
			t.Parallel()

			const sha = "0a1b2c3d4e5f"
			s, _ := newStub(t)
			s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+sha+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{
					{"id": 91, "name": "required", "status": "completed", "conclusion": "success"},
					{"id": 92, "name": "unrelated", "status": "completed", "conclusion": conclusion},
				}})
			})
			s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/92/annotations", func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusInternalServerError, "unrelated annotation lookup failed")
			})
			c, _ := s.client(t)

			checks, err := c.ChecksForCommit(t.Context(), sha, []string{"required"})
			if err != nil {
				t.Fatalf("ChecksForCommit: %v", err)
			}
			if len(checks) != 1 || checks[0].Name != "required" || !checks[0].Green() {
				t.Fatalf("checks = %+v, want only the required green check", checks)
			}
			if got := s.count("GET /repos/" + testOwner + "/" + testRepo + "/check-runs/92/annotations"); got != 0 {
				t.Fatalf("unrelated annotation requests = %d, want none", got)
			}
		})
	}
}

func TestChecksForCommitReturnsARequiredCancelledCheckWithoutReadingFailureDetails(t *testing.T) {
	t.Parallel()

	const sha = "0a1b2c3d4e5f"
	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+sha+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{{
			"id": 91, "name": "required", "status": "completed", "conclusion": "cancelled",
		}}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusInternalServerError, "cancelled check has no failure details")
	})
	c, _ := s.client(t)

	checks, err := c.ChecksForCommit(t.Context(), sha, []string{"required"})
	if err != nil {
		t.Fatalf("ChecksForCommit: %v", err)
	}
	if len(checks) != 1 || !checks[0].Superseded() || checks[0].FailureEvidence != "" || checks[0].FailureFingerprint != "" {
		t.Fatalf("checks = %+v, want one unenriched required cancelled check", checks)
	}
	if got := s.count("GET /repos/" + testOwner + "/" + testRepo + "/check-runs/91/annotations"); got != 0 {
		t.Fatalf("cancelled-check annotation requests = %d, want none", got)
	}
}

func TestChecksForRefFingerprintsFailedOutputAndEveryAnnotation(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+testBranch+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{{
			"id": 91, "name": "test-software-factory", "status": "completed", "conclusion": "failure",
			"output": map[string]any{"title": "tests failed", "summary": "one assertion", "text": "details"},
		}}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			writeJSON(w, http.StatusOK, []map[string]any{{"path": "b_test.go", "start_line": 8, "end_line": 8, "annotation_level": "failure", "message": "second"}})
			return
		}
		w.Header().Set("Link", "<"+s.URL+"/repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations?page=2>; rel=\"next\"")
		writeJSON(w, http.StatusOK, []map[string]any{{"path": "a_test.go", "start_line": 4, "end_line": 4, "annotation_level": "failure", "message": "first"}})
	})
	c, _ := s.client(t)

	checks, err := c.ChecksForRef(t.Context(), testBranch)
	if err != nil {
		t.Fatalf("ChecksForRef: %v", err)
	}
	if len(checks) != 1 || checks[0].Name != "test-software-factory" || checks[0].FailureFingerprint == "" {
		t.Fatalf("checks = %+v, want one failed, fingerprinted check", checks)
	}
	again, err := c.ChecksForRef(t.Context(), testBranch)
	if err != nil {
		t.Fatalf("second ChecksForRef: %v", err)
	}
	if again[0].FailureFingerprint != checks[0].FailureFingerprint {
		t.Fatalf("fingerprint changed between equivalent snapshots: %q then %q", checks[0].FailureFingerprint, again[0].FailureFingerprint)
	}
	if s.count("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations") != 4 {
		t.Fatalf("annotation requests = %d, want both pages for each snapshot; saw %s", s.count("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations"), s)
	}
}

func TestChecksForRefRejectsAPartialAnnotationSnapshot(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+testBranch+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{{
			"id": 91, "name": "test-software-factory", "status": "completed", "conclusion": "failure",
		}}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusInternalServerError, "annotation service unavailable")
	})
	c, _ := s.client(t)

	if _, err := c.ChecksForRef(t.Context(), testBranch); err == nil {
		t.Fatal("ChecksForRef returned a partial check snapshot after annotation retrieval failed")
	}
}

func TestChecksForRefLeavesGenericGitHubActionsFailuresUnfingerprinted(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+testBranch+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{{
			"id": 91, "name": "test-software-factory", "status": "completed", "conclusion": "failure",
		}}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{{
			"path": ".github", "annotation_level": "failure", "message": "Process completed with exit code 1.",
		}})
	})
	c, _ := s.client(t)

	checks, err := c.ChecksForRef(t.Context(), testBranch)
	if err != nil {
		t.Fatalf("ChecksForRef: %v", err)
	}
	if len(checks) != 1 || checks[0].FailureFingerprint != "" {
		t.Fatalf("checks = %+v, want a failed check without a false failure identity", checks)
	}
}

func TestChecksForRefFingerprintsGenericGitHubActionsFailuresFromTestLogs(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+testBranch+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{{
			"id": 91, "name": "test-software-factory", "status": "completed", "conclusion": "failure",
			"details_url": s.URL + "/" + testOwner + "/" + testRepo + "/actions/runs/100/job/91",
		}}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{{
			"path": ".github", "annotation_level": "failure", "message": "Process completed with exit code 1.",
		}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/actions/jobs/91/logs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", s.URL+"/job-91.log")
		w.WriteHeader(http.StatusFound)
	})
	s.handle("GET /job-91.log", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2026-07-30T21:00:00Z --- FAIL: TestWorkTicketContinuesWhenTheSameCheckHasAnotherFailure (0.00s)\n"))
	})
	c, _ := s.client(t)

	checks, err := c.ChecksForRef(t.Context(), testBranch)
	if err != nil {
		t.Fatalf("ChecksForRef: %v", err)
	}
	if len(checks) != 1 || checks[0].FailureFingerprint == "" {
		t.Fatalf("checks = %+v, want the failing Go test identity from the Actions log", checks)
	}
}

func TestFailedGoTestsQualifiesSameNamedTestsByPackage(t *testing.T) {
	t.Parallel()

	pkgA := "2026-07-30T21:00:00Z --- FAIL: TestValidate (0.00s)\n" +
		"2026-07-30T21:00:00Z FAIL\n" +
		"2026-07-30T21:00:00Z FAIL\tgithub.com/0x63616c/world-wide-webb/pkg/a\t0.01s\n"
	pkgB := "2026-07-30T21:00:00Z --- FAIL: TestValidate (0.00s)\n" +
		"2026-07-30T21:00:00Z FAIL\n" +
		"2026-07-30T21:00:00Z FAIL\tgithub.com/0x63616c/world-wide-webb/pkg/b\t0.01s\n"

	testsA := failedGoTests(pkgA)
	testsB := failedGoTests(pkgB)
	if len(testsA) != 1 || len(testsB) != 1 || testsA[0] == testsB[0] {
		t.Fatalf("failedGoTests(pkgA) = %v, failedGoTests(pkgB) = %v, want distinct package-qualified identities", testsA, testsB)
	}
}

func TestFailedGoTestsFallsBackToBareNameWithoutAPackageSummaryLine(t *testing.T) {
	t.Parallel()

	tests := failedGoTests("2026-07-30T21:00:00Z --- FAIL: TestTruncated (0.00s)\n")
	if len(tests) != 1 || tests[0] != "TestTruncated" {
		t.Fatalf("failedGoTests = %v, want the bare test name when no summary line was logged", tests)
	}
}

func TestChecksForRefIgnoresNonFailureAnnotationsAsAnIdentity(t *testing.T) {
	t.Parallel()

	// Every job in this repository carries the runner's standing Node
	// deprecation warning. Fingerprinting it would give the check an
	// identity no code change can move, so rule 1 would read the second red
	// turn as stagnation whatever the agent did.
	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+testBranch+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{{
			"id": 91, "name": "check-software-factory-generated", "status": "completed", "conclusion": "failure",
		}}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{
			{"path": ".github", "start_line": 2, "annotation_level": "warning", "message": "Node.js 20 is deprecated."},
			{"path": ".github", "start_line": 233, "annotation_level": "failure", "message": "Process completed with exit code 1."},
		})
	})
	c, _ := s.client(t)

	checks, err := c.ChecksForRef(t.Context(), testBranch)
	if err != nil {
		t.Fatalf("ChecksForRef: %v", err)
	}
	if len(checks) != 1 || checks[0].FailureFingerprint != "" {
		t.Fatalf("checks = %+v, want a failed check without a false failure identity", checks)
	}
}

func TestChecksForRefFingerprintsNonGoFailuresFromLoggedErrors(t *testing.T) {
	t.Parallel()

	s, _ := newStub(t)
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/commits/"+testBranch+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"check_runs": []map[string]any{{
			"id": 91, "name": "lint", "status": "completed", "conclusion": "failure",
			"details_url": s.URL + "/" + testOwner + "/" + testRepo + "/actions/runs/100/job/91",
		}}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/check-runs/91/annotations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, []map[string]any{{
			"path": ".github", "annotation_level": "failure", "message": "Process completed with exit code 1.",
		}})
	})
	s.handle("GET /repos/"+testOwner+"/"+testRepo+"/actions/jobs/91/logs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", s.URL+"/job-91.log")
		w.WriteHeader(http.StatusFound)
	})
	s.handle("GET /job-91.log", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("2026-07-30T21:00:00Z ##[error]lint/noUnusedVariables: unused import in web/src/app.ts\n" +
			"2026-07-30T21:00:00Z ##[error]Process completed with exit code 1.\n"))
	})
	c, _ := s.client(t)

	checks, err := c.ChecksForRef(t.Context(), testBranch)
	if err != nil {
		t.Fatalf("ChecksForRef: %v", err)
	}
	if len(checks) != 1 || checks[0].FailureFingerprint == "" {
		t.Fatalf("checks = %+v, want the logged error identity for a job that runs no Go tests", checks)
	}
}

func TestLoggedErrorLinesDropsRunnerNoise(t *testing.T) {
	t.Parallel()

	noise := "2026-07-30T21:00:00Z ##[error]Process completed with exit code 1.\n" +
		"2026-07-30T21:00:00Z ##[error]The operation was canceled.\n" +
		"2026-07-30T21:00:00Z ##[error]Canceling since a higher priority waiting request for ci-refs/heads/main exists\n"
	if lines := loggedErrorLines(noise); len(lines) != 0 {
		t.Fatalf("loggedErrorLines = %v, want none: none of these say what failed", lines)
	}

	real := noise + "2026-07-30T21:00:00Z ##[error]drift: internal/api/openapi.yaml differs from the committed copy\n"
	lines := loggedErrorLines(real)
	if len(lines) != 1 || lines[0] != "drift: internal/api/openapi.yaml differs from the committed copy" {
		t.Fatalf("loggedErrorLines = %v, want only the identifying error", lines)
	}
}

func TestIdentifyingAnnotationAcceptsAnySpecificFailure(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		annotation checkAnnotationDetail
		want       bool
	}{
		"node deprecation warning": {checkAnnotationDetail{AnnotationLevel: "warning", Message: "Node.js 20 is deprecated."}, false},
		"stock exit code":          {checkAnnotationDetail{AnnotationLevel: "failure", Message: "Process completed with exit code 1."}, false},
		"stock exit code 2":        {checkAnnotationDetail{AnnotationLevel: "failure", Message: "Process completed with exit code 2."}, false},
		"named assertion":          {checkAnnotationDetail{AnnotationLevel: "failure", Message: "TestValidate: want 200, got 401"}, true},
		"titled exit code":         {checkAnnotationDetail{AnnotationLevel: "failure", Title: "drift", Message: "Process completed with exit code 1."}, true},
	}
	for name, tc := range cases {
		if got := identifyingAnnotation(tc.annotation); got != tc.want {
			t.Errorf("%s: identifyingAnnotation = %v, want %v", name, got, tc.want)
		}
	}
}
