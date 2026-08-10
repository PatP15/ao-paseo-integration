import type { TFunction } from "i18next";

// The remote pane's timeline is the only place a human sees what happened on
// another computer, and it used to show the wire record: an event kind like
// `agent_permission_pending` over a pretty-printed JSON blob. Both are true and
// neither is readable, so each event now gets a sentence, with the raw payload
// kept one click away because this pane is also the audit record.
//
// Both closed sets below come from the backend: the observer's transitions
// (domain/execution_observation.go), the agent's report types
// (domain/execution_report.go, plus the report_gap AO writes itself), and the
// one outbox event AO records when it sends a message
// (domain.ExecutionSessionMessageSent). An unknown kind is not hidden — it
// falls back to the raw string, because a timeline that silently drops an event
// is worse than one that shows a word we have no label for.

/** A human title for one timeline event, or the raw kind if AO has no label. */
export function executionEventTitle(kind: string, t: TFunction): string {
	switch (kind) {
		case "agent_running":
			return t("remoteSession.event.agentRunning");
		case "agent_idle":
			return t("remoteSession.event.agentIdle");
		case "agent_permission_pending":
			return t("remoteSession.event.agentPermissionPending");
		case "agent_error":
			return t("remoteSession.event.agentError");
		case "agent_closed":
			return t("remoteSession.event.agentClosed");
		case "agent_archived":
			return t("remoteSession.event.agentArchived");
		case "session_message_sent":
			return t("remoteSession.event.messageSent");
		case "checkpoint":
			return t("remoteSession.event.checkpoint");
		case "question":
			return t("remoteSession.event.question");
		case "blocked":
			return t("remoteSession.event.blocked");
		case "result":
			return t("remoteSession.event.result");
		case "failure":
			return t("remoteSession.event.failure");
		case "follow_up_proposal":
			return t("remoteSession.event.followUp");
		case "report_gap":
			return t("remoteSession.event.reportGap");
		default:
			return kind;
	}
}

/** How AO learned this event, in the operator's terms rather than the wire's. */
export function executionTransportLabel(transport: string, t: TFunction): string {
	switch (transport) {
		case "terminal":
		case "sentinel":
			return t("remoteSession.transport.agentReport");
		case "output_schema":
			return t("remoteSession.transport.agentOutput");
		case "inspect":
			return t("remoteSession.transport.inspection");
		case "outbox":
			return t("remoteSession.transport.sentByAo");
		default:
			return transport;
	}
}

function readString(payload: Record<string, unknown>, key: string): string {
	const value = payload[key];
	return typeof value === "string" ? value.trim() : "";
}

function readStrings(payload: Record<string, unknown>, key: string): string[] {
	const value = payload[key];
	if (!Array.isArray(value)) return [];
	return value.filter((entry): entry is string => typeof entry === "string" && entry.trim() !== "");
}

/**
 * The one line of prose an event carries, or "" when it has none.
 *
 * Only fields the emitters actually write are read (paseoevent/event.go for the
 * report shapes, the observer for its status payloads), and every one is treated
 * as DATA: it is returned as text for a text node, never as markup.
 */
export function executionEventSummary(kind: string, payloadJson: string, t: TFunction): string {
	let payload: Record<string, unknown>;
	try {
		const parsed: unknown = JSON.parse(payloadJson);
		if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return "";
		payload = parsed as Record<string, unknown>;
	} catch {
		// An unparseable payload is still evidence; the details view shows it raw.
		return "";
	}
	switch (kind) {
		case "session_message_sent":
			return readString(payload, "message");
		case "question": {
			const question = readString(payload, "question");
			const options = readStrings(payload, "options");
			if (options.length === 0) return question;
			return question === ""
				? t("remoteSession.event.optionsLine", { options: options.join(", ") })
				: `${question} — ${t("remoteSession.event.optionsLine", { options: options.join(", ") })}`;
		}
		case "checkpoint":
		case "blocked":
		case "result":
		case "failure":
			return readString(payload, "summary");
		case "follow_up_proposal":
			return readString(payload, "title");
		case "report_gap": {
			const after = payload.afterSeq;
			const observed = payload.observedSeq;
			if (typeof after !== "number" || typeof observed !== "number") return "";
			return t("remoteSession.event.reportGapLine", { after, observed });
		}
		case "agent_error":
			return readString(payload, "error") || readString(payload, "status");
		default:
			// The observer's transitions say everything in their title; their
			// payload is machine state (status, worktree, cwd) that the details
			// view already shows verbatim.
			return "";
	}
}
