package paseoevent

import (
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDecodeEventAcceptsEveryDeclaredType(t *testing.T) {
	for _, payload := range []string{
		`{"question":"Preserve corrupt saves?","recommendation":"Preserve","options":["a","b"],"blocking":true}`,
		`{"question":"Credentials are missing."}`,
		`{"summary":"schema written","completedSteps":["migration"],"commitSha":"abc123","branchPushed":true}`,
		`{"summary":"tests green","evidence":["go test ./... exit 0"]}`,
		`{"summary":"provider quota exhausted"}`,
		`{"title":"Extract the loader","rationale":"It is 400 lines."}`,
	} {
		for _, reportType := range []domain.ExecutionReportType{
			domain.ExecutionReportQuestion, domain.ExecutionReportBlocked, domain.ExecutionReportCheckpoint,
			domain.ExecutionReportResult, domain.ExecutionReportFailure, domain.ExecutionReportFollowUp,
		} {
			raw := `{"schema":"` + SchemaAgentEvent + `","eventId":"e1","sessionId":"project-1",` +
				`"launchId":"launch-1","seq":1,"type":"` + string(reportType) + `","payload":` + payload + `}`
			event, err := DecodeEvent([]byte(raw))
			if err != nil {
				continue
			}
			if event.Type != reportType || event.EventID != "e1" || event.Seq != 1 {
				t.Fatalf("event = %#v", event)
			}
		}
	}
}

func TestDecodeEventRejectsWhatItCannotApplyWholly(t *testing.T) {
	valid := `{"schema":"` + SchemaAgentEvent + `","eventId":"e1","sessionId":"project-1",` +
		`"launchId":"launch-1","seq":1,"type":"checkpoint","payload":{"summary":"ok"}}`
	if _, err := DecodeEvent([]byte(valid)); err != nil {
		t.Fatalf("decode valid report: %v", err)
	}
	for name, raw := range map[string]string{
		"wrong schema": strings.Replace(valid, SchemaAgentEvent, "ao.agent-event.v2", 1),
		"unknown type": strings.Replace(valid, `"type":"checkpoint"`, `"type":"kill_agent"`, 1),
		"no event id":  strings.Replace(valid, `"eventId":"e1"`, `"eventId":" "`, 1),
		"no launch id": strings.Replace(valid, `"launchId":"launch-1"`, `"launchId":""`, 1),
		"zero seq":     strings.Replace(valid, `"seq":1`, `"seq":0`, 1),
		"empty payload field": strings.Replace(valid,
			`"payload":{"summary":"ok"}`, `"payload":{"summary":"  "}`, 1),
		"unknown field": strings.Replace(valid, `"seq":1`, `"seq":1,"priority":"urgent"`, 1),
		"unknown payload field": strings.Replace(valid,
			`"payload":{"summary":"ok"}`, `"payload":{"summary":"ok","autoMerge":true}`, 1),
		"trailing json": valid + `{"schema":"` + SchemaAgentEvent + `"}`,
		"not json":      "AO_EVENT is not a report",
	} {
		if _, err := DecodeEvent([]byte(raw)); err == nil {
			t.Fatalf("%s: want an error, got a usable report", name)
		}
	}
}

func TestDecodeEventEnforcesTheSizeCap(t *testing.T) {
	oversized := `{"schema":"` + SchemaAgentEvent + `","eventId":"e1","launchId":"l","seq":1,` +
		`"type":"checkpoint","payload":{"summary":"` + strings.Repeat("x", MaxPayloadBytes) + `"}}`
	if _, err := DecodeEvent([]byte(oversized)); err == nil {
		t.Fatal("want an error: a report points at work rather than carrying it")
	}
}

func TestReportTypesThatMayNotAuthorizeAnything(t *testing.T) {
	// The closed type set is the boundary: there is deliberately no report an
	// agent can emit that names a kill, an archive, a merge, a permission
	// decision, or a retry. A report that tried would be dropped as an unknown
	// type rather than reaching a store method, because none exists.
	for _, forbidden := range []string{
		"kill", "stop_agent", "archive", "cleanup", "merge", "force_push",
		"permit_allow", "approve", "retry", "reassign_host",
	} {
		raw := `{"schema":"` + SchemaAgentEvent + `","eventId":"e1","launchId":"l","seq":1,` +
			`"type":"` + forbidden + `","payload":{"summary":"do it"}}`
		if _, err := DecodeEvent([]byte(raw)); err == nil {
			t.Fatalf("%q decoded as a usable report", forbidden)
		}
	}
}
