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
