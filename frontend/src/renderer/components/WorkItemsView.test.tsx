import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { WorkItemsView } from "./WorkItemsView";

const { getMock, postMock, navigateMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	navigateMock: vi.fn(),
}));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigateMock }));

const base = {
	projectId: "e2e",
	body: "",
	acceptanceCriteria: [],
	allowedScope: [],
	excludedScope: [],
	riskLevel: "normal",
	lifecycleFact: "open" as const,
	priority: 100,
	createdByType: "human",
	sessionIds: [] as string[],
	createdAt: "2026-08-10T06:00:00Z",
	updatedAt: "2026-08-10T06:00:00Z",
};

function renderView(workItems: Array<Record<string, unknown>>) {
	getMock.mockReset().mockResolvedValue({ data: { workItems }, error: undefined });
	postMock.mockReset().mockResolvedValue({ data: {}, error: undefined });
	navigateMock.mockReset();
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<WorkItemsView projectId="e2e" />
		</QueryClientProvider>,
	);
}

describe("WorkItemsView", () => {
	beforeEach(() => {
		getMock.mockReset();
		postMock.mockReset();
		navigateMock.mockReset();
	});

	// Rejecting used to be one click that recorded "Rejected" and a name. The
	// decision is final, so the reason is the only explanation anyone else gets.
	it("asks why before recording a rejection", async () => {
		renderView([{ ...base, id: "wi_1", title: "Rotate the worker credential", approvalState: "draft" }]);
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Reject" }));
		expect(postMock).not.toHaveBeenCalled();

		const dialog = screen.getByRole("dialog", { name: "Reject this work item?" });
		expect(dialog).toBeInTheDocument();
		// The gated primary says what is holding it, like every other one.
		expect(
			screen.getByText(
				"Say why this is being rejected. Without a reason, a rejected item is a dead end for everyone but you.",
			),
		).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Reject work item" })).toBeDisabled();

		await user.type(screen.getByLabelText("Reason"), "  Superseded by wi_9  ");
		await user.click(screen.getByRole("button", { name: "Reject work item" }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/work-items/{id}/approval", {
			params: { path: { id: "wi_1" } },
			body: { decision: "rejected", note: "Superseded by wi_9" },
		});
	});

	// Approving needs no prose, so it stays a single click.
	it("approves without a dialog", async () => {
		renderView([{ ...base, id: "wi_1", title: "Document the dispatch flow", approvalState: "draft" }]);
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Approve" }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/work-items/{id}/approval", {
			params: { path: { id: "wi_1" } },
			body: { decision: "approved", note: undefined },
		});
	});

	it("shows the reason a rejected item carries, and what rejected means", async () => {
		renderView([
			{
				...base,
				id: "wi_2",
				title: "Rewrite the release script",
				approvalState: "rejected",
				approvedBy: "pat",
				decisionNote: "Superseded by wi_9",
			},
		]);

		expect(await screen.findByText("Superseded by wi_9")).toBeInTheDocument();
		expect(
			screen.getByText("Rejected work is never dispatched. Create a new work item if it should go ahead after all."),
		).toBeInTheDocument();
	});

	// A work item that has been dispatched gave no path to the session doing the
	// work — the one thing an operator wants next.
	it("opens the newest session working the item", async () => {
		renderView([
			{
				...base,
				id: "wi_3",
				title: "Write the release notes",
				approvalState: "approved",
				lifecycleFact: "in_progress",
				sessionIds: ["e2e-2", "e2e-3"],
			},
		]);
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Open session" }));

		expect(navigateMock).toHaveBeenCalledWith({
			to: "/projects/$projectId/sessions/$sessionId",
			params: { projectId: "e2e", sessionId: "e2e-3" },
		});
	});

	it("offers no session link before anything has run", async () => {
		renderView([{ ...base, id: "wi_4", title: "Nothing yet", approvalState: "approved" }]);

		expect(await screen.findByText("Nothing yet")).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "Open session" })).not.toBeInTheDocument();
	});
});
