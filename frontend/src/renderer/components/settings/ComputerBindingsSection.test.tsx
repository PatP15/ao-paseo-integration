import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ComputerBindingsSection } from "./ComputerBindingsSection";

const { getMock, putMock } = vi.hoisted(() => ({
	getMock: vi.fn(),
	putMock: vi.fn(),
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		PUT: (...args: unknown[]) => putMock(...args),
	},
	apiErrorMessage: (error: unknown) => String(error),
}));

function renderSection() {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	return render(
		<QueryClientProvider client={queryClient}>
			<ComputerBindingsSection projectId="project-1" />
		</QueryClientProvider>,
	);
}

describe("ComputerBindingsSection", () => {
	beforeEach(() => {
		getMock.mockReset().mockImplementation(async (path: string) => {
			if (path === "/api/v1/execution/hosts") {
				return {
					data: {
						hosts: [{ id: "worker-1", name: "Worker one", enabled: true }],
					},
					error: undefined,
				};
			}
			return {
				data: {
					bindings: [
						{
							projectId: "project-1",
							hostId: "worker-1",
							hostRepoPath: "/old/repo",
							baseBranch: "main",
							priority: 50,
							enabled: true,
							createdAt: "2026-08-09T00:00:00Z",
							updatedAt: "2026-08-09T00:00:00Z",
						},
					],
				},
				error: undefined,
			};
		});
		putMock.mockReset().mockResolvedValue({ data: {}, error: undefined });
	});

	it("edits the checkout, routing priority, and enabled state of an existing binding", async () => {
		renderSection();
		const user = userEvent.setup();

		await user.click(await screen.findByRole("button", { name: "Edit" }));
		expect(await screen.findByRole("heading", { name: "Edit computer binding" })).toBeInTheDocument();

		fireEvent.change(screen.getByLabelText("Checkout path on the computer"), {
			target: { value: "/new/repo" },
		});
		fireEvent.change(screen.getByLabelText("Base branch"), {
			target: { value: "trunk" },
		});
		fireEvent.change(screen.getByLabelText("Routing priority"), {
			target: { value: "7" },
		});
		await user.click(screen.getByLabelText("Route remote work to this computer"));
		await user.click(screen.getByRole("button", { name: "Save changes" }));

		await waitFor(() => expect(putMock).toHaveBeenCalledTimes(1));
		expect(putMock).toHaveBeenCalledWith("/api/v1/execution/projects/{projectId}/hosts/{hostId}", {
			params: { path: { projectId: "project-1", hostId: "worker-1" } },
			body: {
				hostRepoPath: "/new/repo",
				baseBranch: "trunk",
				priority: 7,
				disabled: true,
			},
		});
	});
});
