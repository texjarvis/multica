import {
  queryOptions,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { api } from "../api";
import { useWorkspaceId } from "../hooks";
import type {
  Agent,
  AgentComposioToolkitAllowlistResponse,
  AgentComposioToolkitAllowlistUpdateResponse,
} from "../types";
import { workspaceKeys } from "../workspace/queries";

export const agentComposioToolkitAllowlistKeys = {
  detail: (agentId: string) =>
    ["agents", agentId, "composio-toolkit-allowlist"] as const,
};

/**
 * The only query that may hold raw toolkit slugs. Generic Agent caches remain
 * count/redacted-only; opening the authorized editor performs this explicit
 * audited reveal.
 */
export function agentComposioToolkitAllowlistOptions(
  agentId: string,
) {
  return queryOptions({
    queryKey: agentComposioToolkitAllowlistKeys.detail(agentId),
    queryFn: () => api.getAgentComposioToolkitAllowlist(agentId),
    enabled: !!agentId,
  });
}

type MutationContext = {
  previousAgents?: Agent[];
  previousAllowlist?: AgentComposioToolkitAllowlistResponse;
};

/**
 * Replaces or clears the allowlist through the dedicated human-only endpoint.
 * A non-empty desired list sends explicit replace intent; an empty list sends
 * explicit clear intent. Optimistic raw values stay only in the dedicated
 * query cache, while the generic Agent list receives count/redacted metadata.
 */
export function useUpdateAgentAllowlist(agentId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const agentsKey = workspaceKeys.agents(wsId);
  const allowlistKey = agentComposioToolkitAllowlistKeys.detail(agentId);

  return useMutation<
    AgentComposioToolkitAllowlistUpdateResponse,
    Error,
    string[],
    MutationContext
  >({
    mutationFn: (allowlist) =>
      api.updateAgentComposioToolkitAllowlist(
        agentId,
        allowlist.length > 0
          ? { intent: "replace", toolkit_slugs: allowlist }
          : { intent: "clear" },
      ),
    onMutate: async (allowlist) => {
      await Promise.all([
        qc.cancelQueries({ queryKey: agentsKey }),
        qc.cancelQueries({ queryKey: allowlistKey }),
      ]);
      const previousAgents = qc.getQueryData<Agent[]>(agentsKey);
      const previousAllowlist =
        qc.getQueryData<AgentComposioToolkitAllowlistResponse>(allowlistKey);

      qc.setQueryData<AgentComposioToolkitAllowlistResponse>(allowlistKey, {
        agent_id: agentId,
        toolkit_slugs: [...allowlist],
      });
      qc.setQueryData<Agent[]>(agentsKey, (old) =>
        old?.map((agent) => {
          if (agent.id !== agentId) return agent;
          const {
            composio_toolkit_allowlist: _discardLegacyRawAllowlist,
            ...valueFreeAgent
          } = agent;
          return {
            ...valueFreeAgent,
            composio_toolkit_allowlist_count: allowlist.length,
            composio_toolkit_allowlist_redacted: allowlist.length > 0,
          } as Agent;
        }),
      );
      return { previousAgents, previousAllowlist };
    },
    onError: (_error, _allowlist, context) => {
      if (context?.previousAgents) {
        qc.setQueryData(agentsKey, context.previousAgents);
      }
      if (context?.previousAllowlist) {
        qc.setQueryData(allowlistKey, context.previousAllowlist);
      } else {
        qc.removeQueries({ queryKey: allowlistKey, exact: true });
      }
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: agentsKey });
      qc.invalidateQueries({ queryKey: allowlistKey });
    },
  });
}
