import { createFileRoute } from "@tanstack/react-router";
import { HostDetailView } from "../components/HostDetailView";

export const Route = createFileRoute("/_shell/computers/$hostId")({
	component: HostDetailRoute,
});

function HostDetailRoute() {
	const { hostId } = Route.useParams();
	return <HostDetailView hostId={hostId} />;
}
