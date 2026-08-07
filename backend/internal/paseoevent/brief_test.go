package paseoevent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type memoryBriefStore struct {
	briefs  []domain.SessionBrief
	saves   int
	loadErr error
	saveErr error
}

func (s *memoryBriefStore) GetLatestSessionBrief(_ context.Context, sessionID domain.SessionID) (domain.SessionBrief, bool, error) {
	if s.loadErr != nil {
		return domain.SessionBrief{}, false, s.loadErr
	}
	var latest domain.SessionBrief
	found := false
	for _, brief := range s.briefs {
		if brief.SessionID == sessionID && (!found || brief.Version > latest.Version) {
			latest, found = brief, true
		}
	}
	return latest, found, nil
}

func (s *memoryBriefStore) SaveSessionBrief(_ context.Context, brief domain.SessionBrief) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	for _, existing := range s.briefs {
		if existing.SessionID == brief.SessionID && existing.Version == brief.Version {
			return errors.New("brief version already exists")
		}
	}
	s.saves++
	s.briefs = append(s.briefs, brief)
	return nil
}

func testBriefs(store *memoryBriefStore, nonces ...string) *Briefs {
	index := 0
	return &Briefs{
		store: store,
		now:   func() time.Time { return time.Date(2026, time.March, 1, 9, 0, 0, 0, time.UTC) },
		newID: func() string {
			index++
			return "brief-" + string(rune('0'+index))
		},
		newNonce: func() (string, error) {
			if len(nonces) == 0 {
				return NewNonce()
			}
			nonce := nonces[0]
			if len(nonces) > 1 {
				nonces = nonces[1:]
			}
			return nonce, nil
		},
	}
}

func testBriefRequest() BriefRequest {
	return BriefRequest{
		SessionID: "project-1", WorkItemID: "work-1", ProjectID: "project", HostID: "worker-1",
		LaunchID: "launch-1", Attempt: 1, Branch: "ao/task-1", BaseBranch: "main",
		Provider: "codex", Model: "gpt-5.4", Mode: "auto",
		Goal: "Implement persistent inventory storage.", Policy: DefaultPolicy(),
	}
}

func TestEnsureCommitsOneBriefAndReplaysItOnRedelivery(t *testing.T) {
	store := &memoryBriefStore{}
	briefs := testBriefs(store, testNonce)
	ctx := context.Background()

	first, err := briefs.Ensure(ctx, testBriefRequest())
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	// The outbox may deliver the same start command more than once. A second
	// brief would mean a second nonce, and a report emitted by the first attempt
	// would then be unreadable.
	second, err := briefs.Ensure(ctx, testBriefRequest())
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if store.saves != 1 {
		t.Fatalf("saves = %d, want 1", store.saves)
	}
	if first.ReportNonce != second.ReportNonce || first.BriefID != second.BriefID {
		t.Fatalf("redelivery minted a new contract: %#v vs %#v", first, second)
	}
	row := store.briefs[0]
	if row.Version != 1 || row.SupersedesBriefID != "" || row.ReportNonce != testNonce {
		t.Fatalf("row = %#v", row)
	}
	if row.SchemaVersion != SchemaRunBrief || row.BriefSHA256 == "" {
		t.Fatalf("row = %#v, want a hashed %s brief", row, SchemaRunBrief)
	}
	if !strings.Contains(row.BriefJSON, `"launchId":"launch-1"`) {
		t.Fatalf("brief json = %s", row.BriefJSON)
	}
}

func TestEnsureSupersedesRatherThanRewritesForANewLaunch(t *testing.T) {
	store := &memoryBriefStore{}
	briefs := testBriefs(store, testNonce, "0f0f0f0f0f0f")
	ctx := context.Background()

	first, err := briefs.Ensure(ctx, testBriefRequest())
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	relaunch := testBriefRequest()
	relaunch.LaunchID = "launch-2"
	relaunch.Attempt = 2
	second, err := briefs.Ensure(ctx, relaunch)
	if err != nil {
		t.Fatalf("relaunch ensure: %v", err)
	}
	if second.ReportNonce == first.ReportNonce {
		t.Fatal("a new launch must get its own nonce, or a superseded attempt's reports stay readable")
	}
	if len(store.briefs) != 2 {
		t.Fatalf("briefs = %d, want both versions retained", len(store.briefs))
	}
	newest := store.briefs[1]
	if newest.Version != 2 || newest.SupersedesBriefID != store.briefs[0].ID {
		t.Fatalf("newest = %#v, want version 2 naming its predecessor", newest)
	}
	if store.briefs[0].BriefJSON == newest.BriefJSON && store.briefs[0].ID == newest.ID {
		t.Fatal("the first brief was rewritten")
	}
}

func TestPromptCannotBeIngestedAsAReport(t *testing.T) {
	// The brief has to teach the frame format, and Paseo's curator prefixes only
	// the first line of a multi-line message — so any complete frame in this text
	// is echoed back into the transcript unprefixed and byte-identical to a real
	// report. Without the placeholder, AO ingests its own instructions on the
	// first poll.
	store := &memoryBriefStore{}
	brief, err := testBriefs(store, testNonce).Ensure(context.Background(), testBriefRequest())
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	prompt := brief.Prompt()
	if !strings.Contains(prompt, NoncePlaceholder) {
		t.Fatalf("prompt does not use the placeholder:\n%s", prompt)
	}
	if strings.Contains(prompt, tokenPrefix+brief.ReportNonce) {
		t.Fatalf("prompt spells the live token out, which the transcript would echo:\n%s", prompt)
	}

	result := Decode(brief.ReportNonce, strings.Split(prompt, "\n"))
	if len(result.Payloads) != 0 {
		t.Fatalf("the brief's own text decoded as %d reports", len(result.Payloads))
	}
	if result.Malformed != 0 {
		t.Fatalf("the brief's own text produced %d malformed frames, which would be counted every poll", result.Malformed)
	}
	if result.Incomplete != 0 {
		t.Fatalf("the brief's own text left %d frame groups open, which would pin the cursor", result.Incomplete)
	}
	// The agent still needs the nonce itself, just never adjacent to the token.
	if !strings.Contains(prompt, brief.ReportNonce) {
		t.Fatalf("prompt does not give the agent its nonce:\n%s", prompt)
	}
}

func TestPromptStatesTheIrreversibleRefusals(t *testing.T) {
	store := &memoryBriefStore{}
	brief, err := testBriefs(store, testNonce).Ensure(context.Background(), testBriefRequest())
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	prompt := brief.Prompt()
	for _, want := range []string{
		"Never force-push", "Never merge", "Never create another agent",
		"Never create a schedule", "Reports are advisory",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}
}

func TestEnsureRefusesAnIncompleteRequest(t *testing.T) {
	briefs := testBriefs(&memoryBriefStore{}, testNonce)
	ctx := context.Background()
	for name, mutate := range map[string]func(*BriefRequest){
		"no session": func(r *BriefRequest) { r.SessionID = "" },
		"no launch":  func(r *BriefRequest) { r.LaunchID = "" },
		"no branch":  func(r *BriefRequest) { r.Branch = "" },
		"no goal":    func(r *BriefRequest) { r.Goal = " " },
		"no attempt": func(r *BriefRequest) { r.Attempt = 0 },
	} {
		request := testBriefRequest()
		mutate(&request)
		if _, err := briefs.Ensure(ctx, request); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}

func TestDecodeBriefRejectsAnUnusableContract(t *testing.T) {
	valid, err := json.Marshal(Brief{
		Schema: SchemaRunBrief, BriefID: "brief-1", SessionID: "project-1",
		LaunchID: "launch-1", ReportNonce: testNonce,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := DecodeBrief(string(valid)); err != nil {
		t.Fatalf("decode valid brief: %v", err)
	}
	for name, encoded := range map[string]string{
		"wrong schema": `{"schema":"ao.run-brief.v2","briefId":"b","sessionId":"s","launchId":"l","reportNonce":"a1b2c3d4e5f6"}`,
		"short nonce":  `{"schema":"ao.run-brief.v1","briefId":"b","sessionId":"s","launchId":"l","reportNonce":"abc"}`,
		"no launch":    `{"schema":"ao.run-brief.v1","briefId":"b","sessionId":"s","launchId":"","reportNonce":"a1b2c3d4e5f6"}`,
		"unknown field": `{"schema":"ao.run-brief.v1","briefId":"b","sessionId":"s","launchId":"l",` +
			`"reportNonce":"a1b2c3d4e5f6","surprise":true}`,
	} {
		if _, err := DecodeBrief(encoded); err == nil {
			t.Fatalf("%s: want an error", name)
		}
	}
}
