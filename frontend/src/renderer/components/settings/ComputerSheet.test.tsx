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
});
