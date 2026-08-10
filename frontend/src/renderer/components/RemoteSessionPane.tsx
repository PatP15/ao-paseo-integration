import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, ChevronRight, Monitor } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { executionHostsQueryOptions } from "../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { executionEventSummary, executionEventTitle, executionTransportLabel } from "../lib/execution-events";
import { formatTimeCompact } from "../lib/format-time";
import type { WorkspaceSession } from "../types/workspace";
import { AutoResumeIndicator } from "./AutoResumeIndicator";
import { PendingLine } from "./PendingLine";

type ExecutionEvent = components["schemas"]["ControllersExecutionEventResponse"];

export function executionEventsQueryKey(sessionId: string) {
	return ["execution-events", sessionId] as const;
}

// Poll from the last durable cursor already rendered, draining every page that
// exists at this read. A fixed first-page query would stop advancing forever
// once a session reached the page limit.
export async function fetchExecutionEvents(sessionId: string, known: ExecutionEvent[] = []): Promise<ExecutionEvent[]> {
	const events = [...known];
	let after = known.at(-1)?.id;
	for (;;) {
		const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/execution-events", {
			params: { path: { sessionId }, query: { limit: 500, after } },
		});
		if (error) throw new Error(apiErrorMessage(error));
		events.push(...data.events);
		if (!data.nextAfter) return events;
		after = data.nextAfter;
	}
}

// One message the composer has queued but has not yet seen come back on the
// timeline. It is held in component state rather than written into the events
// cache because that cache's last id is the polling cursor: a synthetic id
// there would be sent to the daemon as `after` and rejected as unknown.
type PendingMessage = { key: string; message: string; commandId?: string };

// commandIdOf reads the command a timeline event announces, which is what tells
// an optimistic row that the durable one has arrived.
function commandIdOf(raw: string): string | undefined {
	try {
		const parsed: unknown = JSON.parse(raw);
		if (parsed !== null && typeof parsed === "object" && "commandId" in parsed) {
			const { commandId } = parsed as { commandId?: unknown };
			return typeof commandId === "string" ? commandId : undefined;
		}
	} catch {
		return undefined;
	}
	return undefined;
}

// The center pane for a session that runs on another machine. There is no
// local PTY to attach: what AO holds is the durable ingested record — agent
// reports over the terminal transport and observer transitions over inspect —
// so that record is what renders, newest last, exactly as it was applied.
export function RemoteSessionPane({
	session,
	topbarActions,
}: {
	session: WorkspaceSession;
	topbarActions?: ReactNode;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const hostId = session.executionHostId ?? "";
	const hostsQuery = useQuery(executionHostsQueryOptions);
	const host = (hostsQuery.data ?? []).find((candidate) => candidate.id === hostId);
	const unreachable = host !== undefined && !host.reachable;

	const eventsQuery = useQuery({
		queryKey: executionEventsQueryKey(session.id),
		queryFn: () =>
			fetchExecutionEvents(
				session.id,
				queryClient.getQueryData<ExecutionEvent[]>(executionEventsQueryKey(session.id)) ?? [],
			),
		refetchInterval: 3000,
		retry: 1,
	});
	const events = eventsQuery.data ?? [];

	const [draft, setDraft] = useState("");
	const [pending, setPending] = useState<PendingMessage[]>([]);
	const [sendError, setSendError] = useState<string | null>(null);
	const sendMutation = useMutation({
		mutationFn: async ({ key, message }: PendingMessage) => {
			const { data, error } = await apiClient.POST("/api/v1/sessions/{sessionId}/execution-messages", {
				params: { path: { sessionId: session.id } },
				body: { message },
			});
			if (error) throw new Error(apiErrorMessage(error));
			return { key, commandId: data.commandId };
		},
		onSuccess: ({ key, commandId }) => {
			setPending((queued) => queued.map((entry) => (entry.key === key ? { ...entry, commandId } : entry)));
			void queryClient.invalidateQueries({ queryKey: executionEventsQueryKey(session.id) });
		},
		onError: (error: unknown, variables) => {
			// The message never became durable, so the optimistic row is a lie:
			// drop it and put the text back where the human can retry or edit it.
			setPending((queued) => queued.filter((entry) => entry.key !== variables.key));
			setDraft((current) => (current === "" ? variables.message : current));
			setSendError(error instanceof Error ? error.message : t("remoteSession.sendFailed"));
		},
	});

	// An optimistic row retires the moment its own durable event is on the
	// timeline; until then the human sees exactly one copy of what they sent.
	const unconfirmed = pending.filter(
		(entry) =>
			entry.commandId === undefined ||
			!events.some(
				(event) => event.kind === "session_message_sent" && commandIdOf(event.payloadJson) === entry.commandId,
			),
	);
	const composerDisabled = unreachable || sendMutation.isPending;

	const submitDraft = () => {
		const message = draft.trim();
		if (message === "" || composerDisabled) return;
		const entry: PendingMessage = { key: `${session.id}:${Date.now()}:${pending.length}`, message };
		setSendError(null);
		setPending((queued) => [...queued, entry]);
		setDraft("");
		sendMutation.mutate(entry);
	};

	return (
		<div className="flex h-full min-h-0 flex-col">
			<div className="center-panel-titlebar flex h-toolbar shrink-0 items-center gap-2 border-b border-border-strong pr-4">
				<span className="inline-flex min-w-0 items-center gap-1.5 pl-3 font-mono text-2xs text-passive">
					<Monitor className="size-icon-2xs shrink-0" aria-hidden="true" />
					<span className="truncate">
						{/* Host attribution, not a status: the pill beside it owns the
						    session's state, and this reads the same on an exited session
						    as on a live one. Same string as the board card's tooltip. */}
						{t("shell.remoteHost", { host: host?.name ?? hostId })}
						{session.executionAttempt !== undefined && session.executionAttempt > 1
							? ` · ${t("remoteSession.attempt", { attempt: session.executionAttempt })}`
							: ""}
					</span>
				</span>
				<AutoResumeIndicator sessionId={session.id} />
				<div className="min-w-0 flex-1" />
				{topbarActions}
			</div>
			{unreachable ? (
				<div className="mx-3 mt-3 flex items-center gap-3 rounded-md border border-border bg-surface px-3 py-2 text-xs text-muted-foreground">
					<AlertTriangle className="size-icon-base shrink-0 text-warning" aria-hidden="true" />
					{/* Invariant 6: unreachability is a fact about the HOST. The agent
					    may still be working; nothing here implies otherwise and nothing
					    here kills anything. */}
					<span className="min-w-0 flex-1">
						{t("remoteSession.unreachable", { host: host?.name ?? hostId })}
					</span>
				</div>
			) : null}
			<div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">
				{eventsQuery.isLoading ? (
					// Every waiting state in AO spins; see PendingLine.
					<PendingLine>{t("remoteSession.loading")}</PendingLine>
				) : eventsQuery.isError ? (
					<p className="text-sm text-error">
						{eventsQuery.error instanceof Error ? eventsQuery.error.message : t("remoteSession.loadFailed")}
					</p>
				) : events.length === 0 && unconfirmed.length === 0 ? (
					<p className="text-sm text-muted-foreground">{t("remoteSession.empty")}</p>
				) : (
					<ol className="mx-auto flex w-full max-w-3xl flex-col gap-2">
						{events.map((event) => (
							<TimelineEvent key={event.id} event={event} />
						))}
						{unconfirmed.map((entry) => (
							<li key={entry.key} className={`${eventCardClass} opacity-70`}>
								<div className="flex items-baseline justify-between gap-2">
									<span className="text-xs font-medium text-foreground">{t("remoteSession.messageQueued")}</span>
									<span className="shrink-0 text-caption text-passive">{t("remoteSession.sending")}</span>
								</div>
								{/* Same treatment as the durable row it becomes, so the message
								    does not visibly reflow when it lands. */}
								<p className="mt-1 whitespace-pre-wrap break-words text-xs text-muted-foreground">
									{entry.message}
								</p>
							</li>
						))}
					</ol>
				)}
			</div>
			<div className="shrink-0 border-t border-border-strong px-4 py-3">
				<form
					className="mx-auto flex w-full max-w-3xl items-center gap-1.5"
					onSubmit={(event) => {
						event.preventDefault();
						submitDraft();
					}}
				>
					<input
						className="settings-inline-input settings-field min-w-0 flex-1"
						value={draft}
						onChange={(event) => setDraft(event.target.value)}
						placeholder={
							unreachable
									? t("remoteSession.composerUnreachable", { host: host?.name ?? hostId })
									: t("remoteSession.composerPlaceholder")
						}
						aria-label={t("remoteSession.composerLabel")}
						disabled={composerDisabled}
					/>
					<button
						type="submit"
						className="settings-option-trigger shrink-0"
						disabled={composerDisabled || draft.trim() === ""}
					>
						{sendMutation.isPending ? t("remoteSession.sending") : t("remoteSession.send")}
					</button>
				</form>
				{sendError ? (
					<p className="mx-auto mt-1.5 w-full max-w-3xl text-caption text-error" role="alert">
						{sendError}
					</p>
				) : null}
			</div>
		</div>
	);
}

const eventCardClass = "rounded-md border border-border bg-surface px-3 py-2";

// One event, read as a sentence: what happened, how AO learned it, when, and the
// prose the emitter attached. The wire record is a click away rather than the
// default view — this pane is the audit trail for work on another machine, so
// nothing is dropped, but a JSON dump per event is not what "what is the agent
// doing?" deserves as an answer.
function TimelineEvent({ event }: { event: ExecutionEvent }) {
	const { t } = useTranslation();
	const [showDetails, setShowDetails] = useState(false);
	const summary = executionEventSummary(event.kind, event.payloadJson, t);

	return (
		<li className={eventCardClass}>
			<div className="flex items-baseline justify-between gap-2">
				<span className="min-w-0 text-xs font-medium text-foreground">{executionEventTitle(event.kind, t)}</span>
				<span className="shrink-0 text-caption text-passive">
					{executionTransportLabel(event.transport, t)} · {formatTimeCompact(event.observedAt)}
				</span>
			</div>
			{/* Payloads are agent-authored or observer-derived DATA. They render as
			    text, never as markup or instructions. */}
			{summary !== "" ? (
				<p className="mt-1 whitespace-pre-wrap break-words text-xs text-muted-foreground">{summary}</p>
			) : null}
			<button
				type="button"
				className="mt-1 inline-flex items-center gap-1 text-caption text-passive hover:text-foreground"
				aria-expanded={showDetails}
				onClick={() => setShowDetails((current) => !current)}
			>
				{showDetails ? (
					<ChevronDown className="size-icon-2xs shrink-0" aria-hidden="true" />
				) : (
					<ChevronRight className="size-icon-2xs shrink-0" aria-hidden="true" />
				)}
				{t("remoteSession.details")}
			</button>
			{showDetails ? (
				<pre className="mt-1 max-h-40 overflow-y-auto whitespace-pre-wrap break-all font-mono text-2xs text-muted-foreground">
					{formatPayload(event.payloadJson)}
				</pre>
			) : null}
		</li>
	);
}

function formatPayload(raw: string): string {
	try {
		return JSON.stringify(JSON.parse(raw), null, 1);
	} catch {
		return raw;
	}
}
