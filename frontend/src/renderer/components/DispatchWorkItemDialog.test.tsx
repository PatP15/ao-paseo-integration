import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { branchNameFor, DispatchWorkItemDialog } from "./DispatchWorkItemDialog";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
}));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));

const workItem = {
	id: "wi_59e62c92-95b7-4887-b37d-89c0f59b7fb9",
	projectId: "e2e",
	title: "Add a --dry-run flag to ao remote dispatch",
	body: "Print the host the router would pick.",
	acceptanceCriteria: [],
	allowedScope: [],
	excludedScope: [],
	riskLevel: "normal",
	approvalState: "approved" as const,
	lifecycleFact: "open" as const,
	priority: 30,
	createdByType: "human",
	sessionIds: [],
	createdAt: "2026-08-10T06:00:00Z",
	updatedAt: "2026-08-10T06:00:00Z",
};

const HOST_LOOP = {
	id: "loop-worker",
	name: "loop worker",
	enabled: true,
	endpoint: "127.0.0.1:6807",
	transport: "lan",
	trustZone: "hobby",
	capabilities: [],
	activeSessions: 1,
	maxConcurrentSessions: 4,
	reachable: true,
	requiresNoMcp: true,
	requiresNoRelay: true,
};

const HOST_OFFICE = { ...HOST_LOOP, id: "office-mac", name: "Office Mac", endpoint: "office-mac:6780" };

function renderDialog(onOpenChange = vi.fn()) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<DispatchWorkItemDialog projectId="e2e" workItem={workItem} open onOpenChange={onOpenChange} />
		</QueryClientProvider>,
	);
	return onOpenChange;
}

describe("branchNameFor", () => {
	// `ao/wi_59e62c92-95b7-488` — a uuid cut mid-group — read as a truncation bug
	// and said nothing about the work.
	it("slugs the title instead of slicing the id", () => {
		expect(branchNameFor(workItem)).toBe("ao/add-a-dry-run-flag-to-ao-remote-dispatch");
	});

	it("falls back to the id when the title cannot slug", () => {
		expect(branchNameFor({ id: "wi_abc", title: "・/・" })).toBe("ao/wi_abc");
	});

	it("never ends the branch in a separator", () => {
		expect(branchNameFor({ id: "wi_abc", title: "Fix the flaky retry test in CI runs now" })).not.toMatch(/-$/);
	});
});

describe("DispatchWorkItemDialog", () => {
	beforeEach(() => {
		getMock.mockReset().mockImplementation((route: string) => {
			if (route === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: {
						hosts: [
							{
								id: "loop-worker",
								name: "loop worker",
								enabled: true,
								endpoint: "127.0.0.1:6807",
								transport: "lan",
								trustZone: "hobby",
								capabilities: [],
								activeSessions: 0,
								maxConcurrentSessions: 4,
								reachable: true,
								requiresNoMcp: true,
								requiresNoRelay: true,
							},
						],
					},
					error: undefined,
				});
			}
			if (route === "/api/v1/execution/bindings") {
				return Promise.resolve({
					data: { bindings: [{ projectId: "e2e", hostId: "loop-worker", enabled: true }] },
					error: undefined,
				});
			}
			return Promise.resolve({ data: { providers: [], skills: [] }, error: undefined });
		});
		postMock.mockReset();
	});

	// Dispatch has five preconditions and used to refuse with nothing but a
	// greyed-out button.
	it("says which precondition is holding Dispatch", async () => {
		renderDialog();

		expect(await screen.findByText("Choose which computer runs this.")).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Dispatch" })).toBeDisabled();
	});

	// F-B's picker leg: the router skips an offline computer and one whose live
	// bindings fill its session cap, so the picker has to say so before the
	// dispatch is attempted rather than after it is refused.
	it("marks a full computer unavailable and refuses to send work there", async () => {
		getMock.mockImplementation((route: string) => {
			if (route === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: {
						hosts: [
							{
								id: "loop-worker",
								name: "loop worker",
								enabled: true,
								endpoint: "127.0.0.1:6807",
								transport: "lan",
								trustZone: "hobby",
								capabilities: [],
								activeSessions: 4,
								maxConcurrentSessions: 4,
								reachable: true,
								requiresNoMcp: true,
								requiresNoRelay: true,
							},
						],
					},
					error: undefined,
				});
			}
			if (route === "/api/v1/execution/bindings") {
				return Promise.resolve({
					data: { bindings: [{ projectId: "e2e", hostId: "loop-worker", enabled: true }] },
					error: undefined,
				});
			}
			return Promise.resolve({ data: { providers: [], skills: [] }, error: undefined });
		});
		renderDialog();
		const user = userEvent.setup();

		expect(
			await screen.findByText(
				"Every computer bound to this project is offline or full. Test its connection, or wait for a running session to finish.",
			),
		).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Computer" }));
		const option = await screen.findByRole("menuitem", { name: "loop worker — busy, 4 of 4 sessions" });
		// Radix marks a disabled item with aria-disabled rather than the form
		// attribute, and stops it receiving the select.
		expect(option).toHaveAttribute("aria-disabled", "true");
	});

	// An offline computer is the other refusal the router makes; the label has to
	// name it, and the capacity line has to show the headroom on a usable one.
	it("marks an offline computer unavailable and shows capacity for a usable one", async () => {
		getMock.mockImplementation((route: string) => {
			if (route === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: {
						hosts: [
							{
								id: "office-mac",
								name: "Office Mac",
								enabled: true,
								endpoint: "office-mac:6780",
								transport: "tailscale",
								trustZone: "work",
								capabilities: [],
								activeSessions: 0,
								maxConcurrentSessions: 3,
								reachable: false,
								requiresNoMcp: true,
								requiresNoRelay: true,
							},
							{
								id: "loop-worker",
								name: "loop worker",
								enabled: true,
								endpoint: "127.0.0.1:6807",
								transport: "lan",
								trustZone: "hobby",
								capabilities: [],
								activeSessions: 1,
								maxConcurrentSessions: 4,
								reachable: true,
								requiresNoMcp: true,
								requiresNoRelay: true,
							},
						],
					},
					error: undefined,
				});
			}
			if (route === "/api/v1/execution/bindings") {
				return Promise.resolve({
					data: {
						bindings: [
							{ projectId: "e2e", hostId: "office-mac", enabled: true },
							{ projectId: "e2e", hostId: "loop-worker", enabled: true },
						],
					},
					error: undefined,
				});
			}
			return Promise.resolve({ data: { providers: [], skills: [] }, error: undefined });
		});
		renderDialog();
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Computer" }));
		expect(await screen.findByRole("menuitem", { name: "Office Mac — offline" })).toHaveAttribute(
			"aria-disabled",
			"true",
		);
		await user.click(screen.getByRole("menuitem", { name: "loop worker" }));

		expect(await screen.findByText("Running 1 of 4 sessions this computer allows.")).toBeInTheDocument();
	});

	// The gate used to be a bare "⚠" appended to the chip's text: meaning carried
	// by a glyph, with nothing for a screen reader to say.
	it("names the policy gate in the chip's accessible label", async () => {
		getMock.mockImplementation((route: string) => {
			if (route === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: {
						hosts: [
							{
								id: "loop-worker",
								name: "loop worker",
								enabled: true,
								endpoint: "127.0.0.1:6807",
								transport: "lan",
								trustZone: "hobby",
								capabilities: [],
								activeSessions: 0,
								maxConcurrentSessions: 4,
								reachable: true,
								requiresNoMcp: true,
								requiresNoRelay: true,
							},
						],
					},
					error: undefined,
				});
			}
			if (route === "/api/v1/execution/bindings") {
				return Promise.resolve({
					data: { bindings: [{ projectId: "e2e", hostId: "loop-worker", enabled: true }] },
					error: undefined,
				});
			}
			if (route === "/api/v1/execution/hosts/{hostId}/inventory") {
				return Promise.resolve({
					data: {
						skills: [
							{ name: "demo-skill", description: "Demo", policyGated: false },
							{ name: "paseo-advisor", description: "Spin up a single agent", policyGated: true },
						],
					},
					error: undefined,
				});
			}
			return Promise.resolve({ data: { providers: [], skills: [] }, error: undefined });
		});
		renderDialog();
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Computer" }));
		await user.click(await screen.findByRole("menuitem", { name: "loop worker" }));

		expect(await screen.findByRole("button", { name: "paseo-advisor (policy-gated)" })).toBeInTheDocument();
		// An ungated skill keeps its plain name as its accessible name.
		expect(screen.getByRole("button", { name: "demo-skill" })).toBeInTheDocument();
	});

	// ---- the policy gate --------------------------------------------------
	// A gated skill is the only place in the app where a click writes an audit
	// row under the operator's name, so every part of it is pinned: what the
	// panel promises, what each action does, what is left behind, and that the
	// decision cannot follow the operator to another computer.

	function mockGate({ twoComputers = false }: { twoComputers?: boolean } = {}) {
		getMock.mockImplementation((route: string) => {
			if (route === "/api/v1/execution/hosts") {
				return Promise.resolve({
					data: {
						hosts: twoComputers ? [HOST_LOOP, HOST_OFFICE] : [HOST_LOOP],
					},
					error: undefined,
				});
			}
			if (route === "/api/v1/execution/bindings") {
				return Promise.resolve({
					data: {
						bindings: twoComputers
							? [
									{ projectId: "e2e", hostId: "loop-worker", enabled: true },
									{ projectId: "e2e", hostId: "office-mac", enabled: true },
								]
							: [{ projectId: "e2e", hostId: "loop-worker", enabled: true }],
					},
					error: undefined,
				});
			}
			if (route === "/api/v1/execution/hosts/{hostId}/inventory") {
				return Promise.resolve({
					data: {
						skills: [
							{ name: "demo-skill", description: "Demo", policyGated: false },
							{ name: "paseo-advisor", description: "Spin up a single agent", policyGated: true },
						],
					},
					error: undefined,
				});
			}
			if (route === "/api/v1/execution/hosts/{hostId}/providers") {
				return Promise.resolve({
					data: {
						providers: [
							{ provider: "claude", label: "Claude", status: "available", models: [], modes: [], defaultMode: "" },
						],
					},
					error: undefined,
				});
			}
			return Promise.resolve({ data: { providers: [], skills: [] }, error: undefined });
		});
	}

	async function openGate(user: ReturnType<typeof userEvent.setup>, computer = "loop worker") {
		await user.click(await screen.findByRole("button", { name: "Computer" }));
		await user.click(await screen.findByRole("menuitem", { name: computer }));
		await user.click(await screen.findByRole("button", { name: "paseo-advisor (policy-gated)" }));
	}

	const promptValue = () => (screen.getByLabelText("Prompt") as HTMLTextAreaElement).value;

	// The panel offered "Enable for this dispatch" above a paragraph explaining
	// that the skill's orchestration is refused there anyway — the one thing the
	// override cannot do. It is an audit fact that "alters nothing about the
	// launch" (storage/sqlite/store/execution_dispatch_store.go), so the action
	// names the only effect it has.
	it("offers to insert a gated skill rather than to enable it", async () => {
		mockGate();
		renderDialog();
		const user = userEvent.setup();

		await openGate(user);

		expect(await screen.findByText('"paseo-advisor" is policy-gated on loop worker.')).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Insert anyway" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: /Enable/ })).not.toBeInTheDocument();
		// The audit consequence belongs BEFORE the click, not only after it.
		expect(screen.getByText(/records an override under your name with the dispatch/)).toBeInTheDocument();
	});

	// The gate's dismissal was labelled "Cancel", a few centimetres from the
	// footer's Cancel, which abandons the whole dispatch.
	it("names the gate's dismissal for its own scope and leaves nothing behind", async () => {
		mockGate();
		renderDialog();
		const user = userEvent.setup();
		await openGate(user);
		const before = promptValue();

		await user.click(screen.getByRole("button", { name: "Don't insert" }));

		expect(screen.queryByText('"paseo-advisor" is policy-gated on loop worker.')).not.toBeInTheDocument();
		expect(promptValue()).toBe(before);
		expect(screen.queryByText("Overrides recorded with this dispatch")).not.toBeInTheDocument();
	});

	// Inserting used to leave no trace at all: the chip looked exactly as before,
	// and the audit row about to be written could be neither seen nor taken back.
	it("shows the recorded override and lets it be withdrawn without losing the text", async () => {
		mockGate();
		renderDialog();
		const user = userEvent.setup();
		await openGate(user);

		await user.click(screen.getByRole("button", { name: "Insert anyway" }));

		expect(promptValue()).toContain('Use the "paseo-advisor" skill');
		expect(await screen.findByText("Overrides recorded with this dispatch")).toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Withdraw the recorded override for paseo-advisor" }));

		expect(screen.queryByText("Overrides recorded with this dispatch")).not.toBeInTheDocument();
		// Withdrawing is about the audit fact, not the prompt text it inserted —
		// which the hint says, and which the operator edits in the prompt.
		expect(promptValue()).toContain('Use the "paseo-advisor" skill');
	});

	it("sends a recorded override with the dispatch", async () => {
		mockGate();
		postMock.mockResolvedValue({
			data: { sessionId: "e2e-4", commandId: "cmd-1", commandState: "pending", hostId: "loop-worker" },
			error: undefined,
		});
		renderDialog();
		const user = userEvent.setup();
		await openGate(user);
		await user.click(screen.getByRole("button", { name: "Insert anyway" }));
		await user.click(screen.getByRole("button", { name: "Provider" }));
		await user.click(await screen.findByRole("menuitem", { name: "Claude" }));

		await user.click(screen.getByRole("button", { name: "Dispatch" }));

		expect(postMock.mock.calls[0][1].body.settings.skillPolicyOverrides).toEqual(["paseo-advisor"]);
	});

	it("stops sending an override the operator withdrew", async () => {
		mockGate();
		postMock.mockResolvedValue({
			data: { sessionId: "e2e-4", commandId: "cmd-1", commandState: "pending", hostId: "loop-worker" },
			error: undefined,
		});
		renderDialog();
		const user = userEvent.setup();
		await openGate(user);
		await user.click(screen.getByRole("button", { name: "Insert anyway" }));
		await user.click(screen.getByRole("button", { name: "Withdraw the recorded override for paseo-advisor" }));
		await user.click(screen.getByRole("button", { name: "Provider" }));
		await user.click(await screen.findByRole("menuitem", { name: "Claude" }));

		await user.click(screen.getByRole("button", { name: "Dispatch" }));

		expect(postMock.mock.calls[0][1].body.settings).toBeUndefined();
	});

	// The audit row records the skill together with the dispatch's hostId, so an
	// override must not survive a change of computer: it would log a decision the
	// operator never made there.
	it("drops an override when the computer changes", async () => {
		mockGate({ twoComputers: true });
		renderDialog();
		const user = userEvent.setup();
		await openGate(user);
		await user.click(screen.getByRole("button", { name: "Insert anyway" }));
		expect(await screen.findByText("Overrides recorded with this dispatch")).toBeInTheDocument();

		await user.click(screen.getByRole("button", { name: "Computer" }));
		await user.click(await screen.findByRole("menuitem", { name: "Office Mac" }));

		expect(screen.queryByText("Overrides recorded with this dispatch")).not.toBeInTheDocument();
	});

	// Every other dialog in the app offers Cancel beside its primary; this one
	// left the header's × as the only way out.
	it("closes from the footer's Cancel", async () => {
		const onOpenChange = renderDialog();
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Cancel" }));

		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(postMock).not.toHaveBeenCalled();
	});
});
