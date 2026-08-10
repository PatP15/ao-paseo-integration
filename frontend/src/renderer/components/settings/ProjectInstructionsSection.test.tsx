import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ProjectInstructionsSection } from "./ProjectInstructionsSection";

const { getMock, postMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
}));

type Binding = {
	hostId: string;
	hostRepoPath: string;
	baseBranch: string;
	inSync: boolean;
	driftedPaths: string[];
	error?: string;
};

/** One bound computer whose checkout matches the committed files exactly. */
const inSyncBinding: Binding = {
	hostId: "worker-1",
	hostRepoPath: "/srv/repo",
	baseBranch: "main",
	inSync: true,
	driftedPaths: [],
};

function mockInstructions(bindings: Binding[]) {
	getMock.mockReset().mockImplementation(async (path: string) => {
		if (path === "/api/v1/execution/hosts") {
			return {
				data: { hosts: [{ id: "worker-1", name: "Worker one", enabled: true }] },
				error: undefined,
			};
		}
		return {
			data: {
				branch: "main",
				files: [{ path: "AGENTS.md", sha256: "abc", content: "# agents" }],
				bindings,
			},
			error: undefined,
		};
	});
}

function renderSection() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>
			<ProjectInstructionsSection projectId="project-1" />
		</QueryClientProvider>,
	);
}

describe("ProjectInstructionsSection binding drift rows", () => {
	beforeEach(() => {
		postMock.mockReset().mockResolvedValue({ data: { binding: inSyncBinding }, error: undefined });
	});

	it("says a matching checkout is in sync, and offers nothing to sync", async () => {
		mockInstructions([inSyncBinding]);
		renderSection();

		expect(await screen.findByText("In sync")).toBeInTheDocument();
		// Named the way every other mention of the same machine names it, once the
		// host registry lands — the raw id is only the documented fallback for a
		// computer that is no longer registered.
		expect(await screen.findByText("Worker one")).toBeInTheDocument();
		expect(screen.queryByText("worker-1")).not.toBeInTheDocument();
		// Nothing to fast-forward, so the action is absent rather than disabled.
		expect(screen.queryByRole("button", { name: "Sync" })).not.toBeInTheDocument();
		expect(screen.queryByText("Unreadable")).not.toBeInTheDocument();
	});

	it("explains what the rows compare and which way Sync moves", async () => {
		mockInstructions([inSyncBinding]);
		renderSection();

		const explanation = await screen.findByText(/How each bound computer's checkout compares/);
		expect(explanation).toBeInTheDocument();
		// The direction and the refusal are both stated: the row's one action
		// writes to another machine.
		expect(explanation.textContent).toContain("fast-forwards that checkout to its upstream");
		expect(explanation.textContent).toContain("never sends the computer's own edits back");
		expect(explanation.textContent).toContain("diverged is refused rather than merged");
	});

	it("counts the drifted files, names them, and fast-forwards that one binding", async () => {
		mockInstructions([
			{
				hostId: "worker-1",
				hostRepoPath: "/srv/repo",
				baseBranch: "main",
				inSync: false,
				driftedPaths: ["AGENTS.md", "CLAUDE.md"],
			},
		]);
		renderSection();
		const user = userEvent.setup();

		expect(await screen.findByText("2 files drifted")).toBeInTheDocument();
		expect(screen.getByText("AGENTS.md, CLAUDE.md")).toBeInTheDocument();
		expect(screen.queryByText("In sync")).not.toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Sync" }));

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/execution/bindings/{projectId}/{hostId}/sync", {
			params: { path: { projectId: "project-1", hostId: "worker-1" } },
		});
	});

	it("leads a failed read with what it means, keeps the channel's words as detail, and offers no sync", async () => {
		mockInstructions([
			{
				hostId: "worker-1",
				hostRepoPath: "/srv/repo",
				baseBranch: "main",
				inSync: false,
				driftedPaths: [],
				error: "Worker one is not answering. Test the connection on that computer, then try again.",
			},
		]);
		renderSection();

		expect(await screen.findByText("Unreadable")).toBeInTheDocument();
		expect(
			await screen.findByText("AO could not read the instruction files on Worker one, so its drift is unknown."),
		).toBeInTheDocument();
		expect(screen.getByText(/is not answering\. Test the connection/)).toBeInTheDocument();
		// Drift is unknown, so there is nothing to fast-forward to: an unreadable
		// row must not offer Sync, and must not claim the checkout drifted.
		expect(screen.queryByRole("button", { name: "Sync" })).not.toBeInTheDocument();
		expect(screen.queryByText("0 files drifted")).not.toBeInTheDocument();
	});

	it("reports a sync refusal on the row instead of failing silently", async () => {
		mockInstructions([
			{
				hostId: "worker-1",
				hostRepoPath: "/srv/repo",
				baseBranch: "main",
				inSync: false,
				driftedPaths: ["AGENTS.md"],
			},
		]);
		postMock.mockReset().mockResolvedValue({
			data: undefined,
			error: "the checkout has diverged from its upstream",
		});
		renderSection();
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Sync" }));

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("diverged");
	});
});
