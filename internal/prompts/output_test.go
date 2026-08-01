package prompts

import (
	"strings"
	"testing"

	"github.com/0x63616c/software-factory/internal/work"
)

func TestDocumentReadsAStagesOutput(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StagePlan, []byte(`{"document":"opened PR #12.\n\nDetail follows."}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if want := "opened PR #12.\n\nDetail follows."; got.Prose() != want {
		t.Errorf("Prose() = %q, want %q", got.Prose(), want)
	}
}

func TestDocumentRefusesAnythingButTheEnvelope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result string
	}{
		{name: "nothing at all", result: ""},
		{name: "not JSON", result: "here is my plan:"},
		{name: "no document field", result: `{"summary":"did the thing"}`},
		{name: "a null document", result: `{"document":null}`},
		{name: "a document that is not a string", result: `{"document":["a","b"]}`},
		{name: "an empty document", result: `{"document":""}`},
		{name: "a document of whitespace", result: `{"document":"  \n\t "}`},
		{name: "a field the envelope does not have", result: `{"document":"d","verdict":"approve"}`},
		{name: "two envelopes concatenated", result: `{"document":"a"}{"document":"b"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A stage whose output cannot be read is a failed stage. Guessing
			// what it meant is how a confidently wrong PR gets opened.
			if _, err := Decode(work.StagePlan, []byte(tc.result)); err == nil {
				t.Fatalf("Decode accepted %q", tc.result)
			}
		})
	}
}

func TestDocumentSaysWhatItWasGivenWhenItCannotReadIt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result string
		want   string
	}{
		{name: "an envelope with no document", result: `{}`, want: "no document field"},
		{
			// It has the field. Saying otherwise sends whoever is debugging
			// the run looking for a schema mismatch that is not there.
			name:   "an envelope whose document is null",
			result: `{"document":null}`,
			want:   "null",
		},
		{name: "an envelope with a field nothing reads", result: `{"document":"d","verdict":"approve"}`, want: "verdict"},
		{name: "a document with nothing in it", result: `{"document":" "}`, want: "empty document"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := Decode(work.StagePlan, []byte(tc.result))
			if err == nil {
				t.Fatalf("Decode accepted %q", tc.result)
			}
			// The operator reading this at 3am has the error and the
			// transcript, and should not need to go and find the value.
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not say %q", err, tc.want)
			}
		})
	}
}

func TestImplementReadsAStagesOutput(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StageImplement, []byte(`{"report":"did the work","blocked":false,"blocked_reason":"","title":"t","body":"b"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	value, ok := got.Value().(work.ImplementOutput)
	if !ok {
		t.Fatalf("Value() = %T, want work.ImplementOutput", got.Value())
	}
	if value.Report != "did the work" || value.Blocked || value.BlockedReason != "" {
		t.Errorf("got %+v, want an unblocked report", value)
	}
}

func TestImplementCarriesBlockedAndItsReason(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StageImplement, []byte(`{"report":"could not finish","blocked":true,"blocked_reason":"needs a human decision","title":"","body":""}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	value := got.Value().(work.ImplementOutput)
	if !value.Blocked || value.BlockedReason != "needs a human decision" {
		t.Errorf("got %+v, want blocked with its reason", value)
	}
}

func TestImplementRefusesAnythingButTheEnvelope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result string
	}{
		{name: "nothing at all", result: ""},
		{name: "not JSON", result: "here is my report:"},
		{name: "no report field", result: `{"blocked":false,"blocked_reason":""}`},
		{name: "a null report", result: `{"report":null,"blocked":false,"blocked_reason":""}`},
		{name: "an empty report", result: `{"report":"","blocked":false,"blocked_reason":""}`},
		{name: "no blocked field", result: `{"report":"r","blocked_reason":""}`},
		{name: "no blocked_reason field", result: `{"report":"r","blocked":false}`},
		{name: "blocked true with an empty reason", result: `{"report":"r","blocked":true,"blocked_reason":""}`},
		{name: "blocked false with a non-empty reason", result: `{"report":"r","blocked":false,"blocked_reason":"why"}`},
		{name: "a field the envelope does not have", result: `{"report":"r","blocked":false,"blocked_reason":"","verdict":"approve"}`},
		{name: "two envelopes concatenated", result: `{"report":"a","blocked":false,"blocked_reason":""}{"report":"b","blocked":false,"blocked_reason":""}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Decode(work.StageImplement, []byte(tc.result)); err == nil {
				t.Fatalf("Decode accepted %q", tc.result)
			}
		})
	}
}

// TestDecodeIsExhaustiveOverPipeline calls Decode for every stage of the
// pipeline against a stage-appropriate fixture, so a fourth stage or a
// mis-wired case shows up here rather than only in a caller that happens to
// exercise that one stage.
func TestDecodeIsExhaustiveOverPipeline(t *testing.T) {
	t.Parallel()

	for _, stage := range work.Pipeline() {
		t.Run(string(stage), func(t *testing.T) {
			t.Parallel()

			var fixture string
			switch stage {
			case work.StagePlan:
				fixture = `{"document":"d"}`
			case work.StageImplement:
				fixture = `{"report":"r","blocked":false,"blocked_reason":"","title":"t","body":"b"}`
			case work.StageReview:
				fixture = `{"document":"d","findings":[]}`
			}

			got, err := Decode(stage, []byte(fixture))
			if err != nil {
				t.Fatalf("Decode(%s): %v", stage, err)
			}
			if got.Stage() != stage {
				t.Errorf("Stage() = %q, want %q", got.Stage(), stage)
			}
		})
	}
}

func TestReviewReadsFindings(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StageReview, []byte(`{"document":"looks good","findings":[`+
		`{"id":"work/control.go-missing-nil-check","blocking":true,"summary":"a nil check is missing"},`+
		`{"id":"work/style-nit","blocking":false,"summary":"a naming nit"}]}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	value, ok := got.Value().(work.ReviewOutput)
	if !ok {
		t.Fatalf("Value() = %T, want work.ReviewOutput", got.Value())
	}
	if len(value.Findings) != 2 {
		t.Fatalf("Findings = %+v, want 2", value.Findings)
	}
	if value.Findings[0].ID != "work/control.go-missing-nil-check" || !value.Findings[0].Blocking {
		t.Errorf("Findings[0] = %+v, want the blocking finding", value.Findings[0])
	}
	if value.Findings[1].Blocking {
		t.Errorf("Findings[1] = %+v, want an advisory finding", value.Findings[1])
	}
	if got := value.BlockingFindingIDs(); len(got) != 1 || got[0] != "work/control.go-missing-nil-check" {
		t.Errorf("BlockingFindingIDs() = %v, want exactly the one blocking id", got)
	}
}

func TestReviewAcceptsNoFindingsAsACleanPass(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StageReview, []byte(`{"document":"clean pass","findings":[]}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	value := got.Value().(work.ReviewOutput)
	if len(value.Findings) != 0 {
		t.Errorf("Findings = %+v, want none", value.Findings)
	}
}

func TestReviewRefusesAFindingWithNoID(t *testing.T) {
	t.Parallel()

	_, err := Decode(work.StageReview, []byte(`{"document":"d","findings":[{"id":"","blocking":true,"summary":"s"}]}`))
	if err == nil {
		t.Fatal("Decode accepted a finding with an empty id; sameness across turns cannot be judged on one")
	}
}

func TestReviewRefusesAnUnknownTopLevelKey(t *testing.T) {
	t.Parallel()

	_, err := Decode(work.StageReview, []byte(`{"document":"d","findings":[],"verdict":"approve"}`))
	if err == nil {
		t.Fatal("Decode accepted a field the review envelope does not have")
	}
}

func TestReviewRefusesAnUnknownFindingKey(t *testing.T) {
	t.Parallel()

	_, err := Decode(work.StageReview, []byte(
		`{"document":"d","findings":[{"id":"f1","blocking":true,"summary":"s","severity":"high"}]}`))
	if err == nil {
		t.Fatal("Decode accepted a finding field the schema does not have")
	}
}

func TestImplementCarriesTitleAndBody(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StageImplement, []byte(
		`{"report":"did the work","blocked":false,"blocked_reason":"","title":"Fix the thing","body":"Fixes #1"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	value := got.Value().(work.ImplementOutput)
	if value.Title != "Fix the thing" || value.Body != "Fixes #1" {
		t.Errorf("got %+v, want the title and body carried through", value)
	}
}

func TestImplementAllowsAnEmptyTitleAndBodyWhenBlocked(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StageImplement, []byte(
		`{"report":"could not finish","blocked":true,"blocked_reason":"needs a human","title":"","body":""}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	value := got.Value().(work.ImplementOutput)
	if value.Title != "" || value.Body != "" {
		t.Errorf("got %+v, want an empty title and body on a blocked turn", value)
	}
}

// TestDecodeReadsTheReviewsVerifiedList holds the field a later review turn
// is shown so it does not re-litigate a file an earlier turn cleared. It is
// optional in the schema, so both branches matter.
func TestDecodeReadsTheReviewsVerifiedList(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StageReview, []byte(
		`{"document":"d","findings":[],"verified":["relay.go: HMAC verified before forwarding"]}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	review, ok := got.Value().(work.ReviewOutput)
	if !ok {
		t.Fatalf("Value() is %T, want work.ReviewOutput", got.Value())
	}
	if len(review.Verified) != 1 || review.Verified[0] != "relay.go: HMAC verified before forwarding" {
		t.Errorf("Verified = %+v, want the one entry the envelope carried", review.Verified)
	}
}

// TestDecodeAcceptsAReviewThatVerifiedNothing: verified carries no control
// flow, so a turn that omits it is not a stage that failed. Making it
// required would turn the whole run red over a field nothing branches on.
func TestDecodeAcceptsAReviewThatVerifiedNothing(t *testing.T) {
	t.Parallel()

	got, err := Decode(work.StageReview, []byte(`{"document":"d","findings":[]}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	review, ok := got.Value().(work.ReviewOutput)
	if !ok {
		t.Fatalf("Value() is %T, want work.ReviewOutput", got.Value())
	}
	if review.Verified != nil {
		t.Errorf("Verified = %+v, want nil", review.Verified)
	}
}
