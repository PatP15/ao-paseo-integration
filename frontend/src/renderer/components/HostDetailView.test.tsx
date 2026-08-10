import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { InstructionsTab, PreferencesTab, SchedulesTab, SkillsTab } from "./HostDetailView";

const getMock = vi.fn();
const postMock = vi.fn();
const putMock = vi.fn();
const deleteMock = vi.fn();
// The error helpers behave like the real ones, because what the write tabs are
// checked for here is how they treat a *typed* refusal body.
vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
		PUT: (...args: unknown[]) => putMock(...args),
		DELETE: (...args: unknown[]) => deleteMock(...args),
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

function renderSkills() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>
			<SkillsTab hostId="target" />
		</QueryClientProvider>,
	);
}

describe("HostDetailView skills", () => {
	beforeEach(() => {
		getMock.mockReset();
		postMock.mockReset();
		deleteMock.mockReset().mockResolvedValue({ data: {}, error: undefined });
		getMock.mockImplementation((path: string, options?: { params?: { path?: { hostId?: string } } }) => {
			if (path === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: {
						hosts: [
							{ id: "target", enabled: true },
							{ id: "source", name: "Source", enabled: true },
						],
					},
				});
			}
			const hostId = options?.params?.path?.hostId;
			return Promise.resolve({
				data: {
					refreshed: false,
					skillsAsOf: "2026-08-09T00:00:00Z",
					skills:
						hostId === "source"
							? [
									{
										name: "demo-skill",
										description: "Demo",
										policyGated: false,
									},
								]
							: [],
				},
			});
		});
		postMock.mockResolvedValue({
			data: {
				refreshed: true,
				skillsAsOf: "2026-08-09T00:01:00Z",
				skills: [{ name: "demo-skill", description: "Demo", policyGated: false }],
			},
		});
	});

	it("names the computer that has the missing skill, not its registry id", async () => {
		renderSkills();
		// The row is the only place a comparison names another computer; every
		// other surface prints the display name, so this one has to as well.
		expect(await screen.findByText("demo-skill is not installed here — Source has it.")).toBeInTheDocument();
	});

	// The gate badge used to explain itself only in a `title` tooltip, which is
	// unreachable by touch or keyboard and was the sole account of a warning.
	it("explains the policy gate on screen when a gated skill is listed", async () => {
		getMock.mockImplementation((path: string) => {
			if (path === "/api/v1/execution/hosts") return Promise.resolve({ data: { hosts: [{ id: "target", enabled: true }] } });
			return Promise.resolve({
				data: {
					refreshed: false,
					skillsAsOf: "2026-08-09T00:00:00Z",
					skills: [{ name: "paseo-advisor", description: "Spin up a single agent", policyGated: true }],
				},
			});
		});
		renderSkills();
		expect(await screen.findByText("Policy-gated")).toBeInTheDocument();
		expect(screen.getByText(/A policy-gated skill orchestrates through Paseo/)).toBeInTheDocument();
	});

	it("says nothing about the gate when no skill is gated", async () => {
		getMock.mockImplementation((path: string) => {
			if (path === "/api/v1/execution/hosts") return Promise.resolve({ data: { hosts: [{ id: "target", enabled: true }] } });
			return Promise.resolve({
				data: {
					refreshed: false,
					skillsAsOf: "2026-08-09T00:00:00Z",
					skills: [{ name: "demo-skill", description: "Demo", policyGated: false }],
				},
			});
		});
		renderSkills();
		expect(await screen.findByText("demo-skill")).toBeInTheDocument();
		expect(screen.queryByText(/A policy-gated skill orchestrates/)).not.toBeInTheDocument();
	});

	it("uses the clicked comparison row rather than stale manual-form state", async () => {
		renderSkills();
		expect(await screen.findByText(/demo-skill is not installed here/)).toBeInTheDocument();
		await userEvent.click(screen.getAllByRole("button", { name: "Sync to this computer" })[0]);

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/execution/hosts/{hostId}/skills/{name}/sync", {
			params: { path: { hostId: "target", name: "demo-skill" } },
			body: { source: "source" },
		});
	});
});

describe("HostDetailView schedules", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({
			data: {
				schedules: [
					{
						id: "schedule-1",
						name: "Nightly cleanup",
						cadence: "0 3 * * *",
						target: "claude",
						status: "active",
						policyViolation: true,
					},
				],
			},
			error: undefined,
		});
		deleteMock.mockReset().mockResolvedValue({ data: {}, error: undefined });
	});

	it("says what the tab lists and why a schedule is flagged", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<SchedulesTab hostId="target" />
			</QueryClientProvider>,
		);
		expect(await screen.findByText("Nightly cleanup")).toBeInTheDocument();
		expect(screen.getByText(/Recurring jobs the Paseo daemon runs on this computer/)).toBeInTheDocument();
		expect(screen.getByText(/AO never creates schedules/)).toBeInTheDocument();
	});

	it("requires confirmation before deleting a worker schedule", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<SchedulesTab hostId="target" />
			</QueryClientProvider>,
		);
		await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
		expect(deleteMock).not.toHaveBeenCalled();
		expect(screen.getByRole("dialog", { name: "Delete this schedule?" })).toBeInTheDocument();

		await userEvent.click(screen.getByRole("button", { name: "Delete schedule" }));
		await waitFor(() => expect(deleteMock).toHaveBeenCalledTimes(1));
		expect(deleteMock).toHaveBeenCalledWith(
			"/api/v1/execution/hosts/{hostId}/schedules/{scheduleId}",
			{ params: { path: { hostId: "target", scheduleId: "schedule-1" } } },
		);
	});
});

describe("HostDetailView preferences", () => {
	beforeEach(() => {
		getMock.mockReset().mockImplementation((path: string) => {
			if (path === "/api/v1/execution/hosts/{hostId}/inventory") {
				return Promise.resolve({
					data: {
						refreshed: false,
						skillsAsOf: "2026-08-09T00:00:00Z",
						skills: [],
						prefs: {
							content: JSON.stringify({ providers: { impl: "claude/claude-sonnet-5" }, preferences: [] }),
							sha256: "abc",
							confirmedAt: "2026-08-09T00:00:00Z",
						},
					},
					error: undefined,
				});
			}
			if (path === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: { hosts: [{ id: "target", name: "Target computer", enabled: true }] },
					error: undefined,
				});
			}
			return Promise.resolve({ data: { providers: [] }, error: undefined });
		});
		postMock.mockReset();
		putMock.mockReset();
	});

	// The tab used to be five mono file keys — impl, ui, research, planning,
	// audit — against five model pickers, with nothing saying what each covers
	// or who reads the answer.
	it("names each role and what it covers, keeping the file key visible", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		render(
			<QueryClientProvider client={queryClient}>
				<PreferencesTab hostId="target" />
			</QueryClientProvider>,
		);

		expect(await screen.findByText(/Which provider and model the Paseo skills/)).toBeInTheDocument();
		for (const [name, hint] of [
			["Implementation", "Writing and changing code."],
			["User interface", "Visual design, layout and interface copy."],
			["Research", "Reading code and gathering context before a decision."],
			["Planning", "Breaking work down and sequencing it."],
			["Audit", "Reviewing and verifying finished work."],
		]) {
			expect(screen.getByText(name)).toBeInTheDocument();
			expect(screen.getByText(hint)).toBeInTheDocument();
		}
		// The key is what the file contains, so it stays readable beside the name.
		expect(screen.getByText("impl")).toBeInTheDocument();
		// The picker is labelled by the human name, not the raw key.
		expect(screen.getByRole("button", { name: "Implementation" })).toBeInTheDocument();
	});
});

// Both write tabs send the digest they read, so both can be refused because the
// file moved on the computer in between. What reached the user was the channel's
// own line — two 64-char digests and "re-read before writing", an instruction for
// something neither tab offered.
describe("HostDetailView refused writes", () => {
	const REFUSAL = {
		error: "conflict",
		code: "MAINTENANCE_REFUSED",
		message:
			"drift: the file on disk hashes to ddf0d4ae2e98e24a58b75911caa86764408435385d054a86dc26779b17636294, not the expected b1b8419e7c2fd1e99919eef04fa2fe3d2399517cc833ef5b8dc4d1e4876988fd; re-read before writing",
	};

	beforeEach(() => {
		getMock.mockReset().mockImplementation((path: string) => {
			if (path === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: { hosts: [{ id: "target", name: "Target computer", enabled: true }] },
					error: undefined,
				});
			}
			if (path === "/api/v1/execution/hosts/{hostId}/instructions") {
				return Promise.resolve({
					data: {
						instructions: { content: "# Machine instructions\n", sha256: "abc", exists: true },
					},
					error: undefined,
				});
			}
			return Promise.resolve({ data: { providers: [] }, error: undefined });
		});
		putMock.mockReset().mockResolvedValue({ data: undefined, error: REFUSAL });
	});

	function renderInstructions() {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
		return render(
			<QueryClientProvider client={queryClient}>
				<InstructionsTab hostId="target" />
			</QueryClientProvider>,
		);
	}

	it("says a drifted save was refused, names the computer, and offers the re-read the channel demands", async () => {
		renderInstructions();
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Save to computer" }));

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain(
			"This file changed on Target computer after AO read it, so AO refused the save rather than overwrite whoever edited it.",
		);
		// Re-reading costs the user their text, so it is stated before the button.
		expect(alert.textContent).toContain("Re-reading replaces what is on this tab");
		// The digests stay for the audit, under the sentence rather than as it...
		expect(alert.textContent).toContain("re-read before writing");
		// ...without the code that already chose the sentence stapled to the end.
		expect(alert.textContent).not.toContain("(MAINTENANCE_REFUSED)");

		const readsBefore = getMock.mock.calls.filter(
			(call) => call[0] === "/api/v1/execution/hosts/{hostId}/instructions",
		).length;
		await user.click(screen.getByRole("button", { name: /Re-read from computer/ }));
		await waitFor(() =>
			expect(
				getMock.mock.calls.filter((call) => call[0] === "/api/v1/execution/hosts/{hostId}/instructions").length,
			).toBeGreaterThan(readsBefore),
		);
		// The refusal is cleared by the re-read it asked for, not left on screen.
		await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
	});

	it("says why Save is disabled on an empty file instead of going quiet", async () => {
		renderInstructions();
		const user = userEvent.setup();

		const editor = await screen.findByLabelText("Instructions");
		await user.clear(editor);

		expect(screen.getByRole("button", { name: "Save to computer" })).toBeDisabled();
		expect(screen.getByText(/AO will not write an empty instructions file/)).toBeInTheDocument();
	});

	it("shows an untyped write failure as the sentence the daemon already wrote", async () => {
		putMock.mockReset().mockResolvedValue({
			data: undefined,
			error: { code: "HOST_UNAVAILABLE", message: "Target computer is not answering. Test the connection." },
		});
		renderInstructions();
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Save to computer" }));

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("Target computer is not answering. Test the connection.");
		expect(screen.queryByRole("button", { name: /Re-read from computer/ })).not.toBeInTheDocument();
	});
});
