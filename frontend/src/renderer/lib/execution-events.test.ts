import type { TFunction } from "i18next";
import { describe, expect, it } from "vitest";
import { executionEventSummary, executionEventTitle, executionTransportLabel } from "./execution-events";

// The real t is exercised through the pane's own test; here a stub keeps the
// assertions about WHICH key and WHICH interpolation each event resolves to.
const t = ((key: string, options?: Record<string, unknown>) =>
	options === undefined ? key : `${key}(${JSON.stringify(options)})`) as unknown as TFunction;

describe("executionEventTitle", () => {
	it.each([
		["agent_running", "remoteSession.event.agentRunning"],
		["agent_idle", "remoteSession.event.agentIdle"],
		["agent_permission_pending", "remoteSession.event.agentPermissionPending"],
		["agent_error", "remoteSession.event.agentError"],
		["agent_closed", "remoteSession.event.agentClosed"],
		["agent_archived", "remoteSession.event.agentArchived"],
		["session_message_sent", "remoteSession.event.messageSent"],
		["checkpoint", "remoteSession.event.checkpoint"],
		["question", "remoteSession.event.question"],
		["blocked", "remoteSession.event.blocked"],
		["result", "remoteSession.event.result"],
		["failure", "remoteSession.event.failure"],
		["follow_up_proposal", "remoteSession.event.followUp"],
		["report_gap", "remoteSession.event.reportGap"],
	])("titles %s", (kind, key) => {
		expect(executionEventTitle(kind, "{}", t)).toBe(key);
	});

	// The backend records sentBy expressly so this line can tell an auto-resume
	// apart from a message the operator typed; titling AO's own prompt "You sent
	// a message" credited them with work they were not there for.
	it("names AO as the sender of an auto-resume message", () => {
		expect(
			executionEventTitle("session_message_sent", JSON.stringify({ sentBy: "ao-auto-resume", message: "continue" }), t),
		).toBe("remoteSession.event.autoResumeSent");
	});

	it("keeps the human title for a composed message and for an unreadable payload", () => {
		expect(executionEventTitle("session_message_sent", JSON.stringify({ sentBy: "human" }), t)).toBe(
			"remoteSession.event.messageSent",
		);
		expect(executionEventTitle("session_message_sent", "not json", t)).toBe("remoteSession.event.messageSent");
	});

	// A kind AO has no label for must still be visible: a timeline that drops an
	// event is worse than one showing a word we cannot translate yet.
	it("falls back to the raw kind", () => {
		expect(executionEventTitle("something_new", "{}", t)).toBe("something_new");
	});
});

describe("executionTransportLabel", () => {
	it.each([
		["terminal", "remoteSession.transport.agentReport"],
		["sentinel", "remoteSession.transport.agentReport"],
		["output_schema", "remoteSession.transport.agentOutput"],
		["inspect", "remoteSession.transport.inspection"],
		["outbox", "remoteSession.transport.sentByAo"],
		["future_transport", "future_transport"],
	])("labels %s", (transport, expected) => {
		expect(executionTransportLabel(transport, t)).toBe(expected);
	});
});

describe("executionEventSummary", () => {
	it("shows the text of a message you sent", () => {
		expect(
			executionEventSummary(
				"session_message_sent",
				JSON.stringify({ commandId: "cmd-1", message: "Re-read the checklist.", sentBy: "pat" }),
				t,
			),
		).toBe("Re-read the checklist.");
	});

	it("shows a question with the options the agent offered", () => {
		expect(
			executionEventSummary(
				"question",
				JSON.stringify({ question: "Where should the notes go?", options: ["same file", "MIGRATION.md"] }),
				t,
			),
		).toBe(
			'Where should the notes go? — remoteSession.event.optionsLine({"options":"same file, MIGRATION.md"})',
		);
	});

	it.each([
		["checkpoint", "summary"],
		["blocked", "summary"],
		["result", "summary"],
		["failure", "summary"],
		["follow_up_proposal", "title"],
	])("shows the prose a %s report carries", (kind, field) => {
		expect(executionEventSummary(kind, JSON.stringify({ [field]: "Two of three steps done" }), t)).toBe(
			"Two of three steps done",
		);
	});

	it("says which reports went missing", () => {
		expect(executionEventSummary("report_gap", JSON.stringify({ afterSeq: 1, observedSeq: 3 }), t)).toBe(
			'remoteSession.event.reportGapLine({"after":1,"observed":3})',
		);
	});

	// The observer's own transitions say everything in their title; their payload
	// is machine state, which the details view still shows verbatim.
	it("adds no prose to an observer transition", () => {
		expect(
			executionEventSummary("agent_idle", JSON.stringify({ status: "idle", worktree: "e2e-3:1" }), t),
		).toBe("");
	});

	it("survives a payload that is not a JSON object", () => {
		expect(executionEventSummary("checkpoint", "not json", t)).toBe("");
		expect(executionEventSummary("checkpoint", "[1,2]", t)).toBe("");
		expect(executionEventSummary("checkpoint", "null", t)).toBe("");
	});

	// A wrong-typed field is a malformed payload, not a reason to render
	// "[object Object]" at a human.
	it("ignores a field that is not a string", () => {
		expect(executionEventSummary("checkpoint", JSON.stringify({ summary: { nested: true } }), t)).toBe("");
		expect(executionEventSummary("question", JSON.stringify({ question: "Q?", options: [1, 2] }), t)).toBe("Q?");
	});
});
