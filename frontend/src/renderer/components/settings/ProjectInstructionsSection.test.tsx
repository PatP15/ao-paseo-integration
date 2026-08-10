import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ProjectInstructionsSection } from "./ProjectInstructionsSection";

const { getMock, postMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
}));

// The two error helpers are reimplemented rather than stubbed away, because what
// this suite checks is precisely how the section treats a typed error body: the
// real `apiErrorMessage` staples "(CODE)" onto the message, and the row is
// supposed to strip it once the code has chosen a sentence.
vi.mock("../../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorWithoutCode: (message: string, code: string | undefined) =>
		code && message.endsWith(`(${code})`) ? message.slice(0, -(code.length + 2)).trim() : message,
	apiErrorCode: (error: unknown) =>
		typeof error === "object" && error !== null ? ((error as { code?: string }).code ?? undefined) : undefined,
	apiErrorMessage: (error: unknown) => {
		if (typeof error === "object" && error !== null) {
			const body = error as { code?: string; message?: string };
			if (body.message) return body.code ? `${body.message} (${body.code})` : body.message;
		}
		return String(error);
	},
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

	it("says what a refused fast-forward means before quoting git, and drops the stapled code", async () => {
		mockInstructions([
			{
				hostId: "worker-1",
				hostRepoPath: "/srv/repo",
				baseBranch: "main",
				inSync: false,
				driftedPaths: ["CLAUDE.md"],
			},
		]);
		// The shape the channel really returns: a typed refusal carrying git's own
		// multi-line advice (captured from the rig).
		postMock.mockReset().mockResolvedValue({
			data: undefined,
			error: {
				error: "conflict",
				code: "MAINTENANCE_REFUSED",
				message:
					"git pull --ff-only: hint: Diverging branches can't be fast-forwarded, you need to either:\nhint:\nhint: \tgit merge --no-ff\nfatal: Not possible to fast-forward, aborting.",
			},
		});
		renderSection();
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Sync" }));

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("Worker one has commits in its checkout that are not on the project's branch");
		// "computer" is this app's one name for a registered remote — the refusal
		// must not slip into a synonym the rest of the UI never uses.
		expect(alert.textContent).toContain("AO never merges or rebases another computer's work");
		expect(alert.textContent).not.toContain("machine");
		// git's transcript is kept for the audit, verbatim...
		expect(alert.textContent).toContain("fatal: Not possible to fast-forward, aborting.");
		// ...but the code that chose the sentence is not stapled to the end of it.
		expect(alert.textContent).not.toContain("(MAINTENANCE_REFUSED)");
	});

	it("reports an untyped sync failure on the row instead of failing silently", async () => {
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
