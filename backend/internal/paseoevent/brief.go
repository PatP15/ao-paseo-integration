package paseoevent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// SchemaRunBrief is AO's run-brief schema version.
const SchemaRunBrief = "ao.run-brief.v1"

// Brief is the immutable instruction package for one launch.
//
// It is committed before the remote agent exists, because a task's instructions
// may not live only in a prompt: the transcript that would otherwise be the sole
// record of what an agent was told is not durable across a Paseo daemon restart.
//
// The stored content hash covers this object as serialized. It is deliberately
// not a field inside it — a document cannot contain its own hash.
type Brief struct {
	Schema      string `json:"schema"`
	BriefID     string `json:"briefId"`
	SessionID   string `json:"sessionId"`
	WorkItemID  string `json:"workItemId,omitempty"`
	Attempt     int    `json:"attempt"`
	LaunchID    string `json:"launchId"`
	ReportNonce string `json:"reportNonce"`

	Project   BriefProject   `json:"project"`
	Role      string         `json:"role"`
	Goal      string         `json:"goal"`
	Execution BriefExecution `json:"execution"`
	Policy    BriefPolicy    `json:"policy"`
	Reporting BriefReporting `json:"reporting"`
}

// BriefProject is the repository placement the agent works in.
type BriefProject struct {
	ID         string `json:"id"`
	BaseBranch string `json:"baseBranch"`
	Branch     string `json:"branch"`
}

// BriefExecution is where and how the run was placed. Mode is a
// provider-specific string, not a global enum: Paseo has no permission-mode
// vocabulary of its own and each provider exposes its own list.
type BriefExecution struct {
	Host     string `json:"host"`
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Mode     string `json:"mode,omitempty"`
}

// BriefPolicy is what the run may and may not do. It is stated to the agent and
// enforced by AO; an agent's report can never widen it.
type BriefPolicy struct {
	MaySpawnAgents             bool `json:"maySpawnPaseoAgents"`
	MayCreateSchedules         bool `json:"mayCreatePaseoSchedules"`
	MayPushAssignedBranch      bool `json:"mayPushAssignedBranch"`
	MayCreateDraftPullRequest  bool `json:"mayCreateDraftPullRequest"`
	MayMerge                   bool `json:"mayMerge"`
	MustAskBeforeExternalCalls bool `json:"mustAskBeforeExternalServiceChanges"`
}

// BriefReporting is the report contract: which transport carries reports and
// which rules the emitter must follow. Transport is informational — the frame
// format is identical on every rung of the ladder, so a channel that changes
// mid-flight does not invalidate a brief.
type BriefReporting struct {
	Transport                 string `json:"transport"`
	EventSchema               string `json:"eventSchema"`
	QuestionsMustBlock        bool   `json:"questionsMustBlock"`
	FollowUpsAreProposalsOnly bool   `json:"followUpTasksMustBeProposedOnly"`
}

// DefaultPolicy is the MVP policy: the agent works on its own branch, may open a
// draft pull request, and may do nothing irreversible on its own.
func DefaultPolicy() BriefPolicy {
	return BriefPolicy{
		MayPushAssignedBranch:      true,
		MayCreateDraftPullRequest:  true,
		MustAskBeforeExternalCalls: true,
	}
}

// Prompt renders the instruction text delivered to the agent.
//
// The report token is described as a substitution rather than spelled out
// whole. That is not cosmetic: Paseo's transcript curator prefixes only the
// first line of a multi-line message, so any complete frame in this text is
// echoed back unprefixed and byte-identical to a real report — and AO would
// ingest its own instructions on the first poll. Every example here therefore
// carries the placeholder, and the nonce appears only on its own, never next to
// the token. TestPromptCannotBeIngestedAsAReport holds the line.
func (b Brief) Prompt() string {
	var text strings.Builder
	fmt.Fprintf(&text, "You are an AO %s agent working on branch %s (base %s).\n\n",
		b.Role, b.Project.Branch, b.Project.BaseBranch)
	fmt.Fprintf(&text, "Goal:\n%s\n\n", strings.TrimSpace(b.Goal))

	text.WriteString("Policy, which you may not widen:\n")
	for _, rule := range b.policyLines() {
		fmt.Fprintf(&text, "- %s\n", rule)
	}

	text.WriteString("\nReporting. Write progress, questions, and outcomes as report frames.\n")
	fmt.Fprintf(&text, "A frame is one line, at most %d columns, in exactly this form:\n\n", maxLineWidth)
	fmt.Fprintf(&text, "  %s%s kkk/nnn cccccccc <chunk>%s\n\n", tokenPrefix, NoncePlaceholder, terminator)
	text.WriteString("where:\n")
	fmt.Fprintf(&text, "- %s is replaced by your launch nonce, which is: %s\n", NoncePlaceholder, b.ReportNonce)
	fmt.Fprintf(&text, "- kkk/nnn is the 1-based chunk number and chunk count, three digits each\n")
	fmt.Fprintf(&text, "- cccccccc is the CRC-32 (IEEE) of the whole base64 body, 8 lowercase hex digits\n")
	fmt.Fprintf(&text, "- <chunk> is up to %d characters of the standard base64 encoding of one\n", chunkLen)
	fmt.Fprintf(&text, "  %s JSON object, split across chunks in order; only the last may be short\n", b.Reporting.EventSchema)
	fmt.Fprintf(&text, "- the trailing %s terminates the line and must always be present\n\n", terminator)

	text.WriteString("The JSON object has these fields:\n")
	fmt.Fprintf(&text, "  schema:    %q\n", b.Reporting.EventSchema)
	text.WriteString("  eventId:   a UUID you mint, unique per report; resent reports reuse it\n")
	fmt.Fprintf(&text, "  sessionId: %q\n", b.SessionID)
	fmt.Fprintf(&text, "  launchId:  %q\n", b.LaunchID)
	text.WriteString("  seq:       1, then +1 for every later report of this launch\n")
	text.WriteString("  type:      checkpoint | question | blocked | result | failure | follow_up_proposal\n")
	text.WriteString("  payload:   question/blocked: {question, recommendation?, options?, blocking?}\n")
	text.WriteString("             checkpoint: {summary, completedSteps?, remainingSteps?,\n")
	text.WriteString("                          testEvidence?, commitSha?, branchPushed?}\n")
	text.WriteString("             result/failure: {summary, evidence?}\n")
	text.WriteString("             follow_up_proposal: {title, rationale?}\n\n")
	fmt.Fprintf(&text, "Keep each report under %d bytes: a report points at work, it does not carry it.\n", MaxPayloadBytes)

	text.WriteString("\nRules:\n")
	text.WriteString("- Emit a checkpoint before any long stretch of work and after each milestone.\n")
	if b.Reporting.QuestionsMustBlock {
		text.WriteString("- Ask with a question report and then stop. Do not guess and continue.\n")
	}
	if b.Reporting.FollowUpsAreProposalsOnly {
		text.WriteString("- Extra work you notice is a follow_up_proposal. Do not start it.\n")
	}
	text.WriteString("- Reports are advisory. They do not stop, archive, merge, or approve anything;\n")
	text.WriteString("  AO decides those from its own records and from human decisions.\n")
	return text.String()
}

func (b Brief) policyLines() []string {
	rules := []string{
		fmt.Sprintf("Work only on branch %s.", b.Project.Branch),
	}
	if b.Policy.MayPushAssignedBranch {
		rules = append(rules, "You may push that branch. Never force-push, and never push any other branch.")
	} else {
		rules = append(rules, "Do not push.")
	}
	if b.Policy.MayCreateDraftPullRequest {
		rules = append(rules, "You may open a draft pull request.")
	}
	if !b.Policy.MayMerge {
		rules = append(rules, "Never merge, and never mark a pull request ready.")
	}
	if !b.Policy.MaySpawnAgents {
		rules = append(rules, "Never create another agent.")
	}
	if !b.Policy.MayCreateSchedules {
		rules = append(rules, "Never create a schedule or a heartbeat.")
	}
	if b.Policy.MustAskBeforeExternalCalls {
		rules = append(rules, "Ask before changing anything outside this repository.")
	}
	return rules
}

// BriefStore is the durable brief surface. There is no update method by design:
// a brief is written once, and a correction is a new version.
type BriefStore interface {
	GetLatestSessionBrief(context.Context, domain.SessionID) (domain.SessionBrief, bool, error)
	SaveSessionBrief(context.Context, domain.SessionBrief) error
}

// BriefRequest is what AO knows about a launch before it happens.
type BriefRequest struct {
	SessionID  domain.SessionID
	WorkItemID string
	ProjectID  domain.ProjectID
	HostID     domain.ExecutionHostID
	LaunchID   string
	Attempt    int
	Branch     string
	BaseBranch string
	Provider   string
	Model      string
	Mode       string
	Role       string
	Goal       string
	Policy     BriefPolicy
	Transport  domain.ExecutionEventTransport
}

// Briefs commits briefs before launches.
type Briefs struct {
	store    BriefStore
	now      func() time.Time
	newID    func() string
	newNonce func() (string, error)
}

// NewBriefs constructs the brief service.
func NewBriefs(store BriefStore) *Briefs {
	return &Briefs{store: store, now: time.Now, newID: uuid.NewString, newNonce: NewNonce}
}

// Ensure returns the brief for req's launch, committing it first if the launch
// does not have one yet.
//
// It is safe to call repeatedly, which is what makes it safe to call from the
// outbox: a delivery that crashed after committing the brief and before starting
// the agent replays onto the identical brief and the identical nonce, so a
// report emitted by the first attempt is still readable by the second. A launch
// that genuinely differs from the stored brief gets a new version that names its
// predecessor; nothing is ever overwritten.
func (b *Briefs) Ensure(ctx context.Context, req BriefRequest) (Brief, error) {
	if err := validateBriefRequest(req); err != nil {
		return Brief{}, err
	}
	latest, found, err := b.store.GetLatestSessionBrief(ctx, req.SessionID)
	if err != nil {
		return Brief{}, fmt.Errorf("load latest brief for session %s: %w", req.SessionID, err)
	}
	if found {
		existing, decodeErr := DecodeBrief(latest.BriefJSON)
		if decodeErr != nil {
			return Brief{}, fmt.Errorf("decode stored brief %s: %w", latest.ID, decodeErr)
		}
		if existing.LaunchID == req.LaunchID {
			return existing, nil
		}
	}

	nonce, err := b.newNonce()
	if err != nil {
		return Brief{}, err
	}
	brief := Brief{
		Schema: SchemaRunBrief, BriefID: b.newID(), SessionID: string(req.SessionID),
		WorkItemID: req.WorkItemID, Attempt: req.Attempt, LaunchID: req.LaunchID, ReportNonce: nonce,
		Project: BriefProject{ID: string(req.ProjectID), BaseBranch: req.BaseBranch, Branch: req.Branch},
		Role:    req.Role, Goal: req.Goal,
		Execution: BriefExecution{
			Host: string(req.HostID), Provider: req.Provider, Model: req.Model, Mode: req.Mode,
		},
		Policy: req.Policy,
		Reporting: BriefReporting{
			Transport: string(req.Transport), EventSchema: SchemaAgentEvent,
			QuestionsMustBlock: true, FollowUpsAreProposalsOnly: true,
		},
	}
	if brief.Role == "" {
		brief.Role = "implementer"
	}
	if brief.Reporting.Transport == "" {
		brief.Reporting.Transport = string(domain.ExecutionEventTerminal)
	}
	encoded, err := json.Marshal(brief)
	if err != nil {
		return Brief{}, fmt.Errorf("marshal brief for session %s: %w", req.SessionID, err)
	}
	digest := sha256.Sum256(encoded)
	row := domain.SessionBrief{
		ID: brief.BriefID, SessionID: req.SessionID, Version: latest.Version + 1,
		SchemaVersion: SchemaRunBrief, BriefJSON: string(encoded),
		BriefSHA256: hex.EncodeToString(digest[:]), ReportNonce: nonce,
		CreatedAt: b.now().UTC(), SupersedesBriefID: latest.ID,
	}
	if err := b.store.SaveSessionBrief(ctx, row); err != nil {
		return Brief{}, fmt.Errorf("save brief for session %s: %w", req.SessionID, err)
	}
	return brief, nil
}

// DecodeBrief parses a stored brief.
func DecodeBrief(encoded string) (Brief, error) {
	brief, err := decodeStrict[Brief]([]byte(encoded))
	if err != nil {
		return Brief{}, err
	}
	if brief.Schema != SchemaRunBrief {
		return Brief{}, fmt.Errorf("brief schema is %q, want %q", brief.Schema, SchemaRunBrief)
	}
	if err := ValidateNonce(brief.ReportNonce); err != nil {
		return Brief{}, err
	}
	if brief.LaunchID == "" {
		return Brief{}, fmt.Errorf("brief has no launch id")
	}
	return brief, nil
}

// NewNonce mints a launch nonce.
func NewNonce() (string, error) {
	raw := make([]byte, nonceLen/2)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("mint report nonce: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func validateBriefRequest(req BriefRequest) error {
	for name, value := range map[string]string{
		"session": string(req.SessionID), "launch": req.LaunchID,
		"branch": req.Branch, "goal": req.Goal,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("brief: %s is required", name)
		}
	}
	if req.Attempt < 1 {
		return fmt.Errorf("brief: attempt must be positive")
	}
	return nil
}
