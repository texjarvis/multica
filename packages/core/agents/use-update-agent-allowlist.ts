import {
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
  type MutateOptions,
  type QueryClient,
} from "@tanstack/react-query";
import { useCallback } from "react";
import { api, CommittedResponseUnreadableError } from "../api";
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

export const agentComposioToolkitAllowlistRecoveryKeys = {
  detail: (agentId: string) =>
    ["agents", agentId, "composio-toolkit-allowlist-recovery"] as const,
};

export type AgentComposioToolkitAllowlistRecoveryStatus =
  | "idle"
  | "recovering"
  | "failed";

// A just-completed audited reveal must remain fresh long enough for a fenced
// observer to re-enable without immediately issuing a duplicate GET. Keep the
// window short so ordinary tab revisits still detect changes from other
// devices; successful local writes explicitly invalidate this query anyway.
const ALLOWLIST_REVEAL_FRESHNESS_MS = 5_000;

type AgentComposioToolkitAllowlistRecoveryState = {
  agentId: string;
  generation: number;
  status: AgentComposioToolkitAllowlistRecoveryStatus;
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
    staleTime: ALLOWLIST_REVEAL_FRESHNESS_MS,
  });
}

type MutationContext = {
  agentId: string;
  generation: number;
  agentsKey: readonly unknown[];
  allowlistKey: readonly unknown[];
  previousAgents?: Agent[];
  previousAllowlist?: AgentComposioToolkitAllowlistResponse;
};

type MutationVariables = {
  agentId: string;
  workspaceId: string;
  allowlist: string[];
  generation: number;
};

type ExternalMutationOptions = MutateOptions<
  AgentComposioToolkitAllowlistUpdateResponse,
  Error,
  string[],
  MutationContext
>;

type InternalMutationOptions = MutateOptions<
  AgentComposioToolkitAllowlistUpdateResponse,
  Error,
  MutationVariables,
  MutationContext
>;

function mapMutationOptions(
  allowlist: string[],
  options: ExternalMutationOptions | undefined,
): InternalMutationOptions | undefined {
  if (!options) return undefined;

  return {
    onSuccess: options.onSuccess
      ? (data, _variables, result, context) =>
          options.onSuccess?.(data, allowlist, result, context)
      : undefined,
    onError: options.onError
      ? (error, _variables, result, context) =>
          options.onError?.(error, allowlist, result, context)
      : undefined,
    onSettled: options.onSettled
      ? (data, error, _variables, result, context) =>
          options.onSettled?.(data, error, allowlist, result, context)
      : undefined,
  };
}

type AllowlistMutationClientState = {
  generations: Map<string, number>;
};

// Generation ownership must survive hook rerenders and remounts. A WeakMap
// scopes it to the QueryClient lifetime without retaining old clients.
const mutationClientStates = new WeakMap<
  QueryClient,
  AllowlistMutationClientState
>();

function mutationClientState(qc: QueryClient): AllowlistMutationClientState {
  let state = mutationClientStates.get(qc);
  if (!state) {
    state = { generations: new Map() };
    mutationClientStates.set(qc, state);
  }
  return state;
}

function isCurrentRecovery(
  qc: QueryClient,
  state: AllowlistMutationClientState,
  agentId: string,
  generation: number,
): boolean {
  const recovery =
    qc.getQueryData<AgentComposioToolkitAllowlistRecoveryState>(
      agentComposioToolkitAllowlistRecoveryKeys.detail(agentId),
    );
  return (
    state.generations.get(agentId) === generation &&
    recovery?.agentId === agentId &&
    recovery.generation === generation &&
    recovery.status !== "idle"
  );
}

async function recoverCommittedAllowlist(
  qc: QueryClient,
  state: AllowlistMutationClientState,
  agentId: string,
  generation: number,
): Promise<boolean> {
  const recoveryKey =
    agentComposioToolkitAllowlistRecoveryKeys.detail(agentId);
  const allowlistKey =
    agentComposioToolkitAllowlistKeys.detail(agentId);

  if (!isCurrentRecovery(qc, state, agentId, generation)) {
    return false;
  }
  qc.setQueryData<AgentComposioToolkitAllowlistRecoveryState>(
    recoveryKey,
    { agentId, generation, status: "recovering" },
  );

  try {
    // An active QueryObserver retains its currentResult after removeQueries,
    // so deletion is not an uncertainty boundary. Keep the observer fenced,
    // mark its raw data stale without background refetch, then force one
    // explicit audited GET and await the authoritative result.
    await qc.cancelQueries({ queryKey: allowlistKey, exact: true });
    await qc.invalidateQueries({
      queryKey: allowlistKey,
      exact: true,
      refetchType: "none",
    });
    await qc.fetchQuery({
      ...agentComposioToolkitAllowlistOptions(agentId),
      staleTime: 0,
      retry: false,
    });
  } catch {
    if (isCurrentRecovery(qc, state, agentId, generation)) {
      qc.setQueryData<AgentComposioToolkitAllowlistRecoveryState>(
        recoveryKey,
        { agentId, generation, status: "failed" },
      );
    }
    return false;
  }

  if (!isCurrentRecovery(qc, state, agentId, generation)) {
    return false;
  }
  qc.setQueryData<AgentComposioToolkitAllowlistRecoveryState>(
    recoveryKey,
    { agentId, generation, status: "idle" },
  );
  return true;
}

/**
 * Replaces or clears the allowlist through the dedicated human-only endpoint.
 * A non-empty desired list sends explicit replace intent; an empty list sends
 * explicit clear intent. Optimistic raw values stay only in the dedicated
 * query cache, while the generic Agent list receives count/redacted metadata.
 */
export function useUpdateAgentAllowlist(agentId: string) {
  const qc = useQueryClient();
  const wsId = useWorkspaceId();
  const clientState = mutationClientState(qc);
  const recoveryKey =
    agentComposioToolkitAllowlistRecoveryKeys.detail(agentId);
  const initialRecoveryState = {
    agentId,
    generation: 0,
    status: "idle",
  } as const satisfies AgentComposioToolkitAllowlistRecoveryState;
  const recoveryQuery =
    useQuery<AgentComposioToolkitAllowlistRecoveryState>({
      queryKey: recoveryKey,
      // This cache row is local synchronization state, not a server query.
      // Supplying a self-read queryFn prevents an accidental broad
      // invalidation from producing "Missing queryFn" errors without ever
      // allowing that invalidation to clear a live fence.
      queryFn: () =>
        qc.getQueryData<AgentComposioToolkitAllowlistRecoveryState>(
          recoveryKey,
        ) ?? initialRecoveryState,
      enabled: false,
      gcTime: Infinity,
      initialData: initialRecoveryState,
    });
  // The hook may stay mounted while navigation changes agentId. Mutation
  // callbacks can fire after that rerender, so every request captures its
  // target identity/keys in variables + context. QueryClient-scoped per-agent
  // generations also prevent an older overlapping request from rolling back,
  // invalidating, or clearing a recovery fence owned by a newer request.

  const mutation = useMutation<
    AgentComposioToolkitAllowlistUpdateResponse,
    Error,
    MutationVariables,
    MutationContext
  >({
    // A stable, global scope serializes these rare creator-only full-list
    // writes in invocation order. It must never depend on rendered agentId:
    // TanStack mutates pending Mutation options on observer rerender without
    // reindexing MutationCache scope membership, which can strand A→B→A
    // writes forever. The small cross-agent head-of-line cost is deliberate:
    // TanStack then owns pending/error/callback lifecycle, whereas a parallel
    // QueryClient promise queue would duplicate cancellation, error recovery,
    // cleanup, and observer-disposal semantics at a security boundary.
    scope: { id: "agent-composio-allowlist-writes" },
    mutationFn: ({ agentId: targetAgentId, allowlist }) =>
      api.updateAgentComposioToolkitAllowlist(
        targetAgentId,
        allowlist.length > 0
          ? { intent: "replace", toolkit_slugs: allowlist }
          : { intent: "clear" },
      ),
    onMutate: async ({
      agentId: targetAgentId,
      workspaceId,
      allowlist,
      generation,
    }) => {
      const agentsKey = workspaceKeys.agents(workspaceId);
      const allowlistKey =
        agentComposioToolkitAllowlistKeys.detail(targetAgentId);

      await Promise.all([
        qc.cancelQueries({ queryKey: agentsKey }),
        qc.cancelQueries({ queryKey: allowlistKey }),
      ]);

      const context = {
        agentId: targetAgentId,
        generation,
        agentsKey,
        allowlistKey,
      };
      // cancelQueries is asynchronous. If a newer mutation for this agent
      // overtook this one while cancellation was pending, its optimistic
      // selection is already the only state we may render. The scoped network
      // writes still execute in invocation order, but this superseded mutate
      // performs no cache write and its callbacks are generation-guarded.
      if (clientState.generations.get(targetAgentId) !== generation) {
        return context;
      }

      const previousAgents = qc.getQueryData<Agent[]>(agentsKey);
      const previousAllowlist =
        qc.getQueryData<AgentComposioToolkitAllowlistResponse>(allowlistKey);

      qc.setQueryData<AgentComposioToolkitAllowlistResponse>(allowlistKey, {
        agent_id: targetAgentId,
        toolkit_slugs: [...allowlist],
      });
      qc.setQueryData<Agent[]>(agentsKey, (old) =>
        old?.map((agent) => {
          if (agent.id !== targetAgentId) return agent;
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
      return {
        ...context,
        previousAgents,
        previousAllowlist,
      };
    },
    onError: (
      error,
      { agentId: targetAgentId, generation },
      context,
    ) => {
      if (
        !context ||
        context.agentId !== targetAgentId ||
        generation !== context.generation ||
        clientState.generations.get(targetAgentId) !==
          context.generation
      ) {
        return;
      }

      if (error instanceof CommittedResponseUnreadableError) {
        // The server returned 2xx before its confirmation became unreadable.
        // Fence this exact agent synchronously, then force an audited GET.
        // Stale observer data may remain in memory, but the fence keeps every
        // full-list control locked until authoritative state is installed.
        qc.setQueryData<AgentComposioToolkitAllowlistRecoveryState>(
          agentComposioToolkitAllowlistRecoveryKeys.detail(
            targetAgentId,
          ),
          {
            agentId: targetAgentId,
            generation: context.generation,
            status: "recovering",
          },
        );
        return recoverCommittedAllowlist(
          qc,
          clientState,
          targetAgentId,
          context.generation,
        );
      }

      if (context?.previousAgents) {
        qc.setQueryData(context.agentsKey, context.previousAgents);
      }
      if (context?.previousAllowlist) {
        qc.setQueryData(context.allowlistKey, context.previousAllowlist);
      } else {
        qc.removeQueries({ queryKey: context.allowlistKey, exact: true });
      }
      return;
    },
    onSettled: (_data, error, variables, context) => {
      if (
        context &&
        (context.agentId !== variables.agentId ||
          clientState.generations.get(variables.agentId) !==
            context.generation)
      ) {
        return;
      }
      const agentsKey =
        context?.agentsKey ?? workspaceKeys.agents(variables.workspaceId);
      const allowlistKey =
        context?.allowlistKey ??
        agentComposioToolkitAllowlistKeys.detail(variables.agentId);
      // Committed-uncertain recovery already performed exactly one explicit
      // audited raw reveal. Do not trigger a second background GET (especially
      // after failure); the per-agent fence owns retry and unlock semantics.
      if (error instanceof CommittedResponseUnreadableError) {
        return qc.invalidateQueries({ queryKey: agentsKey });
      }
      return Promise.all([
        qc.invalidateQueries({ queryKey: agentsKey }),
        qc.invalidateQueries({
          queryKey: allowlistKey,
          exact: true,
        }),
      ]);
    },
  });

  const mutationVariables = useCallback(
    (allowlist: string[]): MutationVariables => {
      const generation =
        (clientState.generations.get(agentId) ?? 0) + 1;
      clientState.generations.set(agentId, generation);
      return {
        agentId,
        workspaceId: wsId,
        allowlist,
        generation,
      };
    },
    [agentId, clientState, wsId],
  );

  const mutate = useCallback(
    (allowlist: string[], options?: ExternalMutationOptions) => {
      mutation.mutate(
        mutationVariables(allowlist),
        mapMutationOptions(allowlist, options),
      );
    },
    [mutation, mutationVariables],
  );

  const mutateAsync = useCallback(
    (allowlist: string[], options?: ExternalMutationOptions) =>
      mutation.mutateAsync(
        mutationVariables(allowlist),
        mapMutationOptions(allowlist, options),
      ),
    [mutation, mutationVariables],
  );

  const retryCommitUncertainReveal = useCallback(
    async () => {
      const recovery =
        qc.getQueryData<AgentComposioToolkitAllowlistRecoveryState>(
          agentComposioToolkitAllowlistRecoveryKeys.detail(agentId),
        );
      if (!recovery || recovery.status === "idle") return true;
      return recoverCommittedAllowlist(
        qc,
        clientState,
        agentId,
        recovery.generation,
      );
    },
    [agentId, clientState, qc],
  );

  return {
    ...mutation,
    variables: mutation.variables?.allowlist,
    mutate,
    mutateAsync,
    allowlistRecoveryStatus:
      recoveryQuery.data.status,
    isCommitUncertain: recoveryQuery.data.status !== "idle",
    retryCommitUncertainReveal,
  };
}
