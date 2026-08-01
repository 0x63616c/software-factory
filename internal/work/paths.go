package work

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"strings"

	"go.temporal.io/sdk/converter"
)

// WorkspaceRoot is where a stage's working files live inside the sandbox.
//
// It is part of the contract with the sandbox image rather than a private
// detail of whatever runs the stage: the image's entrypoint creates it, and the
// worker writes into it. Changing it means changing both.
const WorkspaceRoot = "/work"

// PayloadKey names one offloaded Temporal payload.
//
// Namespace and WorkflowID come from the serialisation context the SDK supplies
// when encoding. They are empty when it supplies none, which is supported: a
// payload codec may legitimately be called without context.
//
// Digest is the lowercase SHA-256 hex digest of the exact stored bytes. Content
// addressing makes retry writes idempotent and collapses identical payloads.
type PayloadKey struct {
	Namespace  string
	WorkflowID string
	Digest     string
}

// NewPayloadKey derives a content-addressed key for stored.
func NewPayloadKey(sc converter.SerializationContext, stored []byte) PayloadKey {
	sum := sha256.Sum256(stored)
	key := PayloadKey{Digest: hex.EncodeToString(sum[:])}

	switch context := sc.(type) {
	case converter.WorkflowSerializationContext:
		key.Namespace = context.Namespace
		key.WorkflowID = context.WorkflowID
	case converter.ActivitySerializationContext:
		key.Namespace = context.Namespace
		key.WorkflowID = context.WorkflowID
	}

	if !isPathElement(key.Namespace) || !isPathElement(key.WorkflowID) {
		key.Namespace = ""
		key.WorkflowID = ""
	}

	return key
}

// String renders the payload's validated path within the payloads bucket.
func (k PayloadKey) String() string {
	if k.Namespace == "" || k.WorkflowID == "" {
		return "_unkeyed/" + k.Digest
	}

	return k.Namespace + "/" + k.WorkflowID + "/" + k.Digest
}

func isPathElement(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.Contains(value, "/") && !strings.Contains(value, "\\")
}

// RepoDir is where the ticket's repository is checked out inside the sandbox,
// and the working directory repository tools are confined to.
//
// It is a subdirectory of WorkspaceRoot rather than WorkspaceRoot itself, because
// the sandbox root also holds this run's scaffolding — .exec/ and the per-stage
// prompt, schema and result files. Checking the repository out over the top of
// those would put them inside the git working tree, where `implement` is one
// `git add -A` away from committing a prompt into the branch it pushes.
//
// **Nothing creates this directory ahead of the clone, deliberately.** The
// image cannot: /work is an emptyDir, which masks anything baked under it. The
// container runtime must not: a WORKDIR it has to create inside that emptyDir
// is created as root with mode 0755, and the sandbox uid then cannot write its
// own checkout — a permission error that reads as a broken tool. A directory
// the cloning process creates is owned by that process, so the clone creates
// it, and the image's WORKDIR stays at the group-writable WorkspaceRoot.
const RepoDir = WorkspaceRoot + "/repo"

// GhConfigDir is the gh CLI's config directory inside the sandbox, and
// GhHostsFile the credential file it reads out of it.
//
// gh was put in the sandbox because the old `propose` stage opened the pull
// request with it (#414). The pipeline rewrite (#435) moves PR create/edit to
// workflow code against go-github, so whether the sandbox still needs gh (and
// this credential file) at all is worth re-examining — not resolved here,
// since nothing else about the sandbox's gh usage changed as part of that
// rewrite. It needs its own credential file because it has no code path that
// reads git's: git resolves a token through credential.helper and a
// git-credential-store file, and gh looks only at GH_TOKEN in the environment
// or at this file. The same installation token therefore reaches the sandbox
// twice, in two formats — see clone.go's writeGhCredentials for why the
// environment is the wrong one of the two.
//
// Sibling of RepoDir under WorkspaceRoot for the reason RepoDir gives: RepoDir is
// a git working tree, and a credential file inside one is one `git add -A` away
// from being pushed. NOT $HOME/.config/gh, which is gh's default and would make
// the image's HOME a silent second contract; GH_CONFIG_DIR names it explicitly,
// rather than relying on a process-wide HOME convention.
const (
	GhConfigDir = WorkspaceRoot + "/.gh"

	// GhConfigDirEnv is the environment variable pointing gh at GhConfigDir. Set
	// on every sandbox's template by the composition root.
	GhConfigDirEnv = "GH_CONFIG_DIR"

	// GhHostsFile is the file gh reads a host's token from. The name is gh's,
	// not ours.
	GhHostsFile = GhConfigDir + "/hosts.yml"
)

// StageKey identifies one stage attempt, and is the whole of that identity.
//
// Every deterministic path a stage keys off is derived from these four fields
// and nothing else. That is what makes a stage idempotent under activity retry:
// a rescheduled activity computes the same paths, finds what the previous
// attempt left behind, and resumes instead of restarting.
//
// Turn exists because implement and review each loop under this step's
// pipeline rewrite: RunID and Stage alone collided across turns, which would
// let a Temporal-level retry of one turn's activity resume from a later turn's
// session.id, or a later turn's own StagePaths().Dir. It is 1-indexed — the
// first attempt of a stage in a run is turn 1 — because that is the number a
// status comment shows a human ("implement, turn 3 of 15"), and a stage that
// never loops (plan) simply always runs at turn 1.
//
// None of the four can carry attacker-controlled text — a ticket number and a
// turn are both integers, a Temporal RunID is a UUID, and a Stage is one of
// three constants, so the identity cannot be steered by anything a Ticket
// author writes.
type StageKey struct {
	// Ticket is the GitHub issue number.
	Ticket int
	// RunID is Temporal's RunID for the enclosing workflow run. It scopes the
	// attempt so a retried or re-run ticket stays separately inspectable rather
	// than overwriting its own history.
	RunID string
	Stage Stage
	// Turn is which attempt of Stage this is within RunID, starting at 1.
	Turn int
}

// String names the attempt for logs and errors.
func (k StageKey) String() string {
	return fmt.Sprintf("ticket #%d stage %s turn %d run %s", k.Ticket, k.Stage, k.Turn, k.RunID)
}

// TicketWorkflowID is the Temporal claim for a factory-owned Ticket.
//
// Starting a workflow with this ID *is* the claim: Temporal refuses a second
// execution with an open run under the same ID, so uniqueness here replaces a
// lease table or an advisory lock. Nothing else may construct this string — a
// second spelling would be a second claim.
//
// Its `factory-ticket-` prefix is deliberately disjoint from the retired
// `work-ticket-` scheme (#559): Temporal lets a closed run's ID be reused, so
// sharing that namespace would have let a small Ticket id share a history
// lineage with the GitHub issue of the same number.
func TicketWorkflowID(ticketID int64) string {
	return fmt.Sprintf("factory-ticket-%d", ticketID)
}

// FactoryTicketBranchName names a Ticket-backed run's branch.
func FactoryTicketBranchName(ticketID int64, runID string) string {
	return path.Join("software-factory", "factory-ticket-"+strconv.FormatInt(ticketID, 10), runID)
}

// factoryTicketBranchPrefix is FactoryTicketBranchName's own middle segment
// prefix, named once so the parser below cannot drift from the constructor it
// inverts.
const factoryTicketBranchPrefix = "factory-ticket-"

// ParseFactoryTicketBranchName recovers the TicketID FactoryTicketBranchName
// encoded into a branch name, or false if branch was not built by that
// function.
//
// branch is attacker-controllable — it arrives off a GitHub pull_request
// webhook payload, which anyone who can open a pull request against this repo
// controls. Parsing it this strictly (three slash-separated segments, an
// exact literal prefix, a decimal integer with no sign or leading zero) means
// a crafted branch name can only ever resolve to a genuine positive TicketID
// or fail closed; it can never be coerced into resolving to the wrong Ticket
// or into anything this package's callers would have to sanitise further.
func ParseFactoryTicketBranchName(branch string) (ticketID int64, ok bool) {
	parts := strings.Split(branch, "/")
	if len(parts) != 3 || parts[0] != "software-factory" || parts[2] == "" {
		return 0, false
	}
	digits, hasPrefix := strings.CutPrefix(parts[1], factoryTicketBranchPrefix)
	if !hasPrefix || digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// FactoryTicketBranchBelongsToRun reports whether branch is the canonical branch for runID.
func FactoryTicketBranchBelongsToRun(branch, runID string) bool {
	ticketID, ok := ParseFactoryTicketBranchName(branch)
	return ok && branch == FactoryTicketBranchName(ticketID, runID)
}

// TargetDispatcherWorkflowID is the stable singleton target Dispatcher ID.
const TargetDispatcherWorkflowID = "software-factory-target-dispatcher"

// MaintainFactoryScheduleID is the stable Temporal Schedule reconciled on
// every activated worker boot.
const MaintainFactoryScheduleID = "software-factory-maintain"

// MaintainFactoryWorkflowID is the business ID prefix for executions started
// by MaintainFactoryScheduleID. Temporal may append a timestamp for uniqueness.
const MaintainFactoryWorkflowID = "software-factory-maintain"
