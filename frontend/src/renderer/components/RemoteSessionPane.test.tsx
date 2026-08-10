import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import { fetchExecutionEvents, RemoteSessionPane } from "./RemoteSessionPane";

const getMock = vi.fn();
vi.mock("../lib/api-client", () => ({
	apiClient: { GET: (...args: unknown[]) => getMock(...args) },
	apiErrorMessage: (error: unknown) => String(error),
}));
vi.mock("./AutoResumeIndicator", () => ({ AutoResumeIndicator: () => null }));

function event(id: string) {
	return {
		id,
		sessionId: "session-1",
		hostId: "host-1",
		kind: "checkpoint",
		transport: "terminal" as const,
		payloadJson: "{}",
		observedAt: "2026-08-09T00:00:00Z",
		ingestedAt: "2026-08-09T00:00:00Z",
		applied: true,
	};
}

describe("fetchExecutionEvents", () => {
	beforeEach(() => getMock.mockReset());

	it("continues from the rendered cursor and drains every returned page", async () => {
		getMock
			.mockResolvedValueOnce({
				data: { events: [event("event-501")], nextAfter: "event-501" },
			})
			.mockResolvedValueOnce({ data: { events: [event("event-502")] } });

		await expect(fetchExecutionEvents("session-1", [event("event-500")])).resolves.toEqual([
			event("event-500"),
			event("event-501"),
			event("event-502"),
		]);
		expect(getMock).toHaveBeenNthCalledWith(1, "/api/v1/sessions/{sessionId}/execution-events", {
			params: {
				path: { sessionId: "session-1" },
				query: { limit: 500, after: "event-500" },
			},
		});
		expect(getMock).toHaveBeenNthCalledWith(2, "/api/v1/sessions/{sessionId}/execution-events", {
			params: {
				path: { sessionId: "session-1" },
				query: { limit: 500, after: "event-501" },
			},
		});
	});

	it("surfaces a later-page failure instead of freezing the timeline", async () => {
		getMock
			.mockResolvedValueOnce({
				data: { events: [event("event-1")], nextAfter: "event-1" },
			})
			.mockResolvedValueOnce({ error: { message: "host unavailable" } });
		await expect(fetchExecutionEvents("session-1")).rejects.toThrow("[object Object]");
	});
});

const session: WorkspaceSession = {
	id: "e2e-3",
	workspaceId: "e2e",
	workspaceName: "e2e",
	title: "release notes",
	provider: "claude-code",
	status: "idle",
	updatedAt: "2026-08-10T00:00:00Z",
	executionHostId: "loop-worker",
	prs: [],
};

function renderPane(events: Array<Record<string, unknown>>) {
	getMock.mockReset().mockImplementation((path: string) => {
		if (path === "/api/v1/execution/hosts") {
			return Promise.resolve({
				data: { hosts: [{ id: "loop-worker", name: "loop worker", enabled: true, reachable: true }] },
				error: undefined,
			});
		}
		return Promise.resolve({ data: { events }, error: undefined });
	});
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	render(
		<QueryClientProvider client={queryClient}>
			<RemoteSessionPane session={session} />
		</QueryClientProvider>,
	);
}

describe("RemoteSessionPane timeline", () => {
	// The timeline used to be the wire record: `agent_idle` over a JSON blob,
	// `inspect` as the provenance. True, unreadable, and the only view a human
	// had of work on another machine.
	it("reads each event as a sentence instead of dumping its JSON", async () => {
		renderPane([
			{
				id: "event-1",
				sessionId: "e2e-3",
				hostId: "loop-worker",
				kind: "session_message_sent",
				transport: "outbox",
				payloadJson: JSON.stringify({ commandId: "cmd-1", message: "Re-read the checklist." }),
				observedAt: "2026-08-10T00:00:00Z",
				ingestedAt: "2026-08-10T00:00:00Z",
				applied: true,
			},
			{
				id: "event-2",
				sessionId: "e2e-3",
				hostId: "loop-worker",
				kind: "agent_idle",
				transport: "inspect",
				payloadJson: JSON.stringify({ status: "idle", worktree: "e2e-3:1" }),
				observedAt: "2026-08-10T00:01:00Z",
				ingestedAt: "2026-08-10T00:01:00Z",
				applied: true,
			},
		]);

		expect(await screen.findByText("You sent a message")).toBeInTheDocument();
		expect(screen.getByText("Re-read the checklist.")).toBeInTheDocument();
		expect(screen.getByText("Agent went idle")).toBeInTheDocument();
		// Provenance in the operator's terms, not the transport enum.
		expect(screen.getByText(/sent by AO/)).toBeInTheDocument();
		expect(screen.getByText(/AO inspection/)).toBeInTheDocument();
		expect(screen.queryByText("agent_idle")).not.toBeInTheDocument();
		// The wire record is not gone — this pane is an audit trail — it is just
		// no longer the default view.
		expect(screen.queryByText(/"worktree"/)).not.toBeInTheDocument();
	});

	it("keeps the raw payload one click away", async () => {
		renderPane([
			{
				id: "event-1",
				sessionId: "e2e-3",
				hostId: "loop-worker",
				kind: "agent_idle",
				transport: "inspect",
				payloadJson: JSON.stringify({ status: "idle", worktree: "e2e-3:1" }),
				observedAt: "2026-08-10T00:00:00Z",
				ingestedAt: "2026-08-10T00:00:00Z",
				applied: true,
			},
		]);

		const toggle = await screen.findByRole("button", { name: "Details" });
		expect(toggle).toHaveAttribute("aria-expanded", "false");
		await userEvent.click(toggle);

		expect(toggle).toHaveAttribute("aria-expanded", "true");
		expect(screen.getByText(/"worktree": "e2e-3:1"/)).toBeInTheDocument();
	});
});
