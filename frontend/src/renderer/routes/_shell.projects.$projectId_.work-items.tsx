import { createFileRoute } from "@tanstack/react-router";
import { WorkItemsView } from "../components/WorkItemsView";

export const Route = createFileRoute("/_shell/projects/$projectId_/work-items")({
	component: WorkItemsRoute,
});

function WorkItemsRoute() {
	const { projectId } = Route.useParams();
	return <WorkItemsView projectId={projectId} />;
}
