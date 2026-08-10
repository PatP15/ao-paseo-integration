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
