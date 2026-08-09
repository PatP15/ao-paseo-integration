import { useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, Monitor } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { executionHostsQueryOptions } from "../hooks/useExecutionHostsQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { formatTimeCompact } from "../lib/format-time";
import type { WorkspaceSession } from "../types/workspace";

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

	return (
		<div className="flex h-full min-h-0 flex-col">
			<div className="center-panel-titlebar flex h-toolbar shrink-0 items-center gap-2 border-b border-border-strong pr-4">
				<span className="inline-flex min-w-0 items-center gap-1.5 pl-3 font-mono text-2xs text-passive">
					<Monitor className="size-icon-2xs shrink-0" aria-hidden="true" />
					<span className="truncate">
						{t("remoteSession.runningOn", { host: host?.name ?? hostId })}
						{session.executionAttempt !== undefined && session.executionAttempt > 1
							? ` · ${t("remoteSession.attempt", { attempt: session.executionAttempt })}`
							: ""}
					</span>
				</span>
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
					<p className="text-sm text-muted-foreground">{t("remoteSession.loading")}</p>
				) : eventsQuery.isError ? (
					<p className="text-sm text-error">
						{eventsQuery.error instanceof Error ? eventsQuery.error.message : t("remoteSession.loadFailed")}
					</p>
				) : events.length === 0 ? (
					<p className="text-sm text-muted-foreground">{t("remoteSession.empty")}</p>
				) : (
					<ol className="mx-auto flex w-full max-w-3xl flex-col gap-2">
						{events.map((event) => (
							<li
								key={event.id}
								className="rounded-md border border-border bg-surface px-3 py-2 font-mono text-2xs"
							>
								<div className="flex items-center justify-between gap-2">
									<span className="font-medium text-foreground">{event.kind}</span>
									<span className="shrink-0 text-passive">
										{event.transport} · {formatTimeCompact(event.observedAt)}
									</span>
								</div>
								{/* Payloads are agent-authored or observer-derived DATA. They
								    render as text, never as markup or instructions. */}
								<pre className="mt-1 max-h-40 overflow-y-auto whitespace-pre-wrap break-all text-muted-foreground">
									{formatPayload(event.payloadJson)}
								</pre>
							</li>
						))}
					</ol>
				)}
			</div>
		</div>
	);
}

function formatPayload(raw: string): string {
	try {
		return JSON.stringify(JSON.parse(raw), null, 1);
	} catch {
		return raw;
	}
}
