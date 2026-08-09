import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SkillsTab } from "./HostDetailView";

const getMock = vi.fn();
const postMock = vi.fn();
vi.mock("../lib/api-client", () => ({
	apiClient: {
		GET: (...args: unknown[]) => getMock(...args),
		POST: (...args: unknown[]) => postMock(...args),
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

	it("uses the clicked comparison row rather than stale manual-form state", async () => {
		renderSkills();
		expect(await screen.findByText(/lacks demo-skill/)).toBeInTheDocument();
		await userEvent.click(screen.getAllByRole("button", { name: "Sync to this computer" })[0]);

		await waitFor(() => expect(postMock).toHaveBeenCalledTimes(1));
		expect(postMock).toHaveBeenCalledWith("/api/v1/execution/hosts/{hostId}/skills/{name}/sync", {
			params: { path: { hostId: "target", name: "demo-skill" } },
			body: { source: "source" },
		});
	});
});
