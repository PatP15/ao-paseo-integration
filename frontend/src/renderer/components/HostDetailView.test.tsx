import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { PreferencesTab, SchedulesTab, SkillsTab } from "./HostDetailView";

const getMock = vi.fn();
const postMock = vi.fn();
const deleteMock = vi.fn();
vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
		DELETE: (...args: unknown[]) => deleteMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
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
			return Promise.resolve({ data: { providers: [] }, error: undefined });
		});
		postMock.mockReset();
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
