import { useQuery } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";

export type ExecutionHost = components["schemas"]["ExecutionHostResponse"];

export const executionHostsQueryKey = ["execution-hosts"] as const;

async function fetchExecutionHosts(): Promise<ExecutionHost[]> {
	const { data, error } = await apiClient.GET("/api/v1/execution/hosts");
	if (error) throw new Error(apiErrorMessage(error));
	return data.hosts;
}

export const executionHostsQueryOptions = {
	queryKey: executionHostsQueryKey,
	queryFn: fetchExecutionHosts,
	retry: 1,
	staleTime: 30 * 1000,
};

/**
 * The name a computer is called everywhere else in the app, from its id.
 *
 * Surfaces that only carry the id — a session card's Monitor row, a project's
 * per-binding drift row — were printing the raw id (`loop-worker`) beside
 * panes and settings rows naming the same machine `loop worker`, which reads as
 * two different computers. Falls back to the id, so a host that has since been
 * removed still identifies itself instead of rendering blank.
 */
export function useExecutionHostName(hostId: string | undefined): string {
	const { data } = useQuery(executionHostsQueryOptions);
	if (!hostId) return "";
	return data?.find((host) => host.id === hostId)?.name ?? hostId;
}
