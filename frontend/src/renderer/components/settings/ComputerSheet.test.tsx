import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ComputerSheet } from "./ComputerSheet";

const { getMock, postMock, putMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	postMock: vi.fn(),
	putMock: vi.fn(),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
		PUT: (...args: unknown[]) => putMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
}));

function renderSheet() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>
			<ComputerSheet open onOpenChange={vi.fn()} host={null} />
		</QueryClientProvider>,
	);
}

describe("ComputerSheet", () => {
	beforeEach(() => {
		getMock.mockReset().mockResolvedValue({
			data: {
				hosts: [
					{
						id: "worker",
						name: "Worker",
						enabled: true,
						endpoint: "worker:6807",
						transport: "lan",
						trustZone: "work",
						capabilities: [],
						activeSessions: 0,
						maxConcurrentSessions: 2,
						requiresNoMcp: true,
						requiresNoRelay: true,
					},
				],
			},
			error: undefined,
		});
		postMock.mockReset();
		putMock.mockReset();
	});

	// Registration is an upsert with no host delete to undo it, and the secret
	// write happens first, so an id collision would rotate the credential of a
	// computer the operator never meant to touch.
	it("refuses to add a computer under an id that is already registered", async () => {
		renderSheet();
		const user = userEvent.setup();

		// The id is derived from the endpoint, so a second daemon on the same
		// machine reaches the taken id without the operator typing it.
		await user.type(screen.getByLabelText("Endpoint"), "worker:6808");

		expect(await screen.findByText(/already registered/)).toBeInTheDocument();
		expect(screen.getByLabelText("Computer ID")).toHaveAttribute("aria-invalid", "true");
		await user.click(screen.getByRole("button", { name: "Next" }));
		expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
		expect(postMock).not.toHaveBeenCalled();
		expect(putMock).not.toHaveBeenCalled();
	});

	it("accepts a free id and carries it into the details step", async () => {
		renderSheet();
		const user = userEvent.setup();

		await user.type(screen.getByLabelText("Endpoint"), "builder:6807");

		await waitFor(() => expect(screen.getByLabelText("Computer ID")).toHaveValue("builder"));
		expect(screen.queryByText(/already registered/)).not.toBeInTheDocument();
		await user.click(screen.getByRole("button", { name: "Next" }));
		expect(await screen.findByLabelText("Display name")).toBeInTheDocument();
	});

	// The empty step opened with Next already disabled and nothing on screen
	// naming the field that was holding it.
	it("names the field that is blocking Next on each step", async () => {
		renderSheet();
		const user = userEvent.setup();

		expect(screen.getByText("Enter the computer's endpoint to continue.")).toBeInTheDocument();
		await user.type(screen.getByLabelText("Endpoint"), "builder");
		expect(await screen.findByText(/endpoint needs a port/)).toBeInTheDocument();

		await user.type(screen.getByLabelText("Endpoint"), ":6807");
		await waitFor(() => expect(screen.queryByText(/endpoint needs a port/)).not.toBeInTheDocument());
		await user.click(screen.getByRole("button", { name: "Next" }));

		expect(await screen.findByText("Give this computer a display name to continue.")).toBeInTheDocument();
		await user.type(screen.getByLabelText("Display name"), "Builder");
		await waitFor(() => expect(screen.getByRole("button", { name: "Next" })).toBeEnabled());
	});

	// The --no-mcp posture is a precondition AO enforces, so it starts confirmed;
	// unticking it still blocks the step, but now says so.
	it("starts with the --no-mcp confirmation checked and explains unticking it", async () => {
		renderSheet();
		const user = userEvent.setup();

		await user.type(screen.getByLabelText("Endpoint"), "builder:6807");
		await user.click(screen.getByRole("button", { name: "Next" }));
		await user.type(await screen.findByLabelText("Display name"), "Builder");

		const noMcp = screen.getByRole("checkbox");
		expect(noMcp).toBeChecked();
		expect(screen.getByRole("button", { name: "Next" })).toBeEnabled();

		await user.click(noMcp);
		expect(await screen.findByText(/only dispatches to a computer whose daemon was started with --no-mcp/)).toBeInTheDocument();
		expect(screen.getByRole("button", { name: "Next" })).toBeDisabled();
	});
});
