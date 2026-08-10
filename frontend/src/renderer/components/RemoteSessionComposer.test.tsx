import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { WorkspaceSession } from "../types/workspace";
import { RemoteSessionPane } from "./RemoteSessionPane";

const getMock = vi.fn();
const postMock = vi.fn();
vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
}));

const session = {
	id: "project-1",
	workspaceId: "project",
	workspaceName: "project",
	title: "Implement work",
	provider: "claude-code",
	status: "working",
	updatedAt: "2026-08-10T00:00:00Z",
	executionHostId: "worker-1",
} as unknown as WorkspaceSession;

function messageEvent(commandId: string) {
	return {
		id: `event-${commandId}`,
		sessionId: "project-1",
		hostId: "worker-1",
		kind: "session_message_sent",
		transport: "outbox" as const,
		payloadJson: JSON.stringify({ commandId, message: "rerun the failing test", sentBy: "human" }),
		observedAt: "2026-08-10T00:01:00Z",
		ingestedAt: "2026-08-10T00:01:00Z",
		applied: true,
	};
}

// events is mutable so a test can make the poll return the durable row that
// retires the optimistic one.
let events: ReturnType<typeof messageEvent>[] = [];
let reachable = true;

function renderPane() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return render(
		<QueryClientProvider client={queryClient}>
			<RemoteSessionPane session={session} />
		</QueryClientProvider>,
	);
}

describe("RemoteSessionPane composer", () => {
	beforeEach(() => {
		getMock.mockReset();
		postMock.mockReset();
		events = [];
		reachable = true;
		getMock.mockImplementation((path: string) => {
			if (path === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: { hosts: [{ id: "worker-1", name: "office mac", reachable }] },
					error: undefined,
				});
			}
			return Promise.resolve({ data: { events }, error: undefined });
		});
		postMock.mockResolvedValue({ data: { commandId: "command-1" }, error: undefined });
	});

	it("posts the typed message to the session's execution-message route", async () => {
		renderPane();
		const input = await screen.findByLabelText("Message the remote agent");
		await userEvent.type(input, "rerun the failing test");
		await userEvent.click(screen.getByRole("button", { name: "Send" }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		const [path, options] = postMock.mock.calls[0] as [string, { params: { path: { sessionId: string } }; body: { message: string } }];
		expect(path).toBe("/api/v1/sessions/{sessionId}/execution-messages");
		expect(options.params.path).toEqual({ sessionId: "project-1" });
		expect(options.body).toEqual({ message: "rerun the failing test" });
	});

	it("shows the message on the timeline before the daemon has confirmed it", async () => {
		renderPane();
		await userEvent.type(await screen.findByLabelText("Message the remote agent"), "rerun the failing test");
		await userEvent.click(screen.getByRole("button", { name: "Send" }));

		expect(await screen.findByText("message queued")).toBeInTheDocument();
		// Exactly one copy: the optimistic row must retire when its own durable
		// event arrives, not sit alongside it.
		events = [messageEvent("command-1")];
		await waitFor(() => expect(screen.queryByText("message queued")).not.toBeInTheDocument(), { timeout: 5000 });
		expect(screen.getAllByText(/rerun the failing test/)).toHaveLength(1);
	});

	it("drops the optimistic row and restores the draft when the send fails", async () => {
		postMock.mockResolvedValue({ error: "host refused" });
		renderPane();
		const input = await screen.findByLabelText("Message the remote agent");
		await userEvent.type(input, "rerun the failing test");
		await userEvent.click(screen.getByRole("button", { name: "Send" }));

		expect(await screen.findByRole("alert")).toHaveTextContent("host refused");
		expect(screen.queryByText("message queued")).not.toBeInTheDocument();
		expect(input).toHaveValue("rerun the failing test");
	});

	it("disables the composer while the computer is not answering", async () => {
		reachable = false;
		renderPane();
		const input = await screen.findByLabelText("Message the remote agent");
		await waitFor(() => expect(input).toBeDisabled());
		expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
		// The banner above names the computer; the composer has to name the same
		// thing the same way, and by display name rather than registry id.
		expect(input).toHaveAttribute(
			"placeholder",
			"office mac is not answering — messages cannot be queued right now.",
		);
	});

	it("attributes the session to its computer without claiming it is running", async () => {
		renderPane();
		// "Runs on X" is the board card's wording too: an attribution that stays
		// true after the session exits, next to the pill that owns the status.
		expect(await screen.findByText("Runs on office mac")).toBeInTheDocument();
		expect(screen.queryByText(/Running on/)).not.toBeInTheDocument();
	});
});
