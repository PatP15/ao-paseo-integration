import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { CreateWorkItemDialog } from "./WorkItemsView";

const { postMock } = vi.hoisted(() => ({ postMock: vi.fn() }));

vi.mock("../lib/api-client", () => ({
	apiClient: { POST: (...args: unknown[]) => postMock(...args) },
	apiErrorMessage: (error: unknown) => String(error),
}));

function renderDialog(onOpenChange = vi.fn()) {
	const queryClient = new QueryClient({
		defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
	});
	render(
		<QueryClientProvider client={queryClient}>
			<CreateWorkItemDialog projectId="e2e" open onOpenChange={onOpenChange} />
		</QueryClientProvider>,
	);
	return onOpenChange;
}

describe("CreateWorkItemDialog", () => {
	beforeEach(() => postMock.mockReset().mockResolvedValue({ data: {}, error: undefined }));

	// The submit used to repeat the dialog's own title ("New work item"), which
	// says what the dialog is rather than what the button does. Every sibling
	// dialog's primary is a verb: Dispatch, Bind computer, Start task.
	it("names the action on the primary instead of repeating the dialog title", () => {
		renderDialog();
		expect(screen.getByRole("button", { name: "Create work item" })).toBeInTheDocument();
		expect(screen.queryByRole("button", { name: "New work item" })).not.toBeInTheDocument();
	});

	it("offers Cancel beside the primary, the way every other dialog does", async () => {
		const onOpenChange = renderDialog();
		await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
		expect(onOpenChange).toHaveBeenCalledWith(false);
		expect(postMock).not.toHaveBeenCalled();
	});

	it("still creates the work item from the typed title", async () => {
		renderDialog();
		await userEvent.type(screen.getByLabelText("Title"), "Rotate the credential");
		await userEvent.click(screen.getByRole("button", { name: "Create work item" }));
		expect(postMock).toHaveBeenCalledWith("/api/v1/work-items", {
			body: { projectId: "e2e", title: "Rotate the credential", body: "" },
		});
	});
});
