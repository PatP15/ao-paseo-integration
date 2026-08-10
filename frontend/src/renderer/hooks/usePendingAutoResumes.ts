import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type PendingAutoResume = components["schemas"]["PendingAutoResume"];

export const pendingAutoResumesQueryKey = ["auto-resume-pending"] as const;

// One read answers every card on the board. Asking per session would put the
// query count on the size of the board for something that is empty almost all
// of the time.
export function usePendingAutoResumes() {
	return useQuery({
		queryKey: pendingAutoResumesQueryKey,
		queryFn: async (): Promise<PendingAutoResume[]> => {
			const { data, error } = await apiClient.GET("/api/v1/settings/auto-resume/pending");
			if (error) throw new Error(apiErrorMessage(error));
			return data.pending;
		},
		// The row disappears when the watcher delivers the resume, which it does
		// on its own poll loop; a minute of lag on a wait measured in hours is
		// not worth a busier refetch.
		refetchInterval: 60 * 1000,
		staleTime: 30 * 1000,
		// A daemon too old to know the route must not paint every card red.
		retry: false,
	});
}

/** The pending resume for one session, or undefined when it has none. */
export function usePendingAutoResume(sessionId: string): PendingAutoResume | undefined {
	const { data } = usePendingAutoResumes();
	return data?.find((row) => row.sessionId === sessionId);
}
