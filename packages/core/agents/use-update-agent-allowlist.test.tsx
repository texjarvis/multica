/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import {
  QueryClient,
  QueryClientProvider,
  useQuery,
} from "@tanstack/react-query";
import type { ReactNode } from "react";
import {
  CommittedResponseUnreadableError,
  setApiInstance,
} from "../api";
import type { ApiClient } from "../api/client";
import type { Agent } from "../types";
import { workspaceKeys } from "../workspace/queries";
import {
  agentComposioToolkitAllowlistOptions,
  agentComposioToolkitAllowlistKeys,
  useUpdateAgentAllowlist,
} from "./use-update-agent-allowlist";

vi.mock("../hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

function createWrapper(qc: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  };
}

const agent = {
  id: "agent-1",
  composio_toolkit_allowlist_count: 1,
  composio_toolkit_allowlist_redacted: true,
} as Agent;

describe("useUpdateAgentAllowlist", () => {
  let qc: QueryClient;
  const update = vi.fn();
  const reveal = vi.fn();

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    });
    update.mockReset();
    reveal.mockReset();
    reveal.mockResolvedValue({
      agent_id: "agent-1",
      toolkit_slugs: ["notion"],
    });
    update.mockImplementation(
      async (
        agentId: string,
        body: { intent: "replace" | "clear"; toolkit_slugs?: string[] },
      ) => ({
        agent_id: agentId,
        toolkit_count: body.toolkit_slugs?.length ?? 0,
      }),
    );
    setApiInstance({
      getAgentComposioToolkitAllowlist: reveal,
      updateAgentComposioToolkitAllowlist: update,
    } as unknown as ApiClient);
    qc.setQueryData(workspaceKeys.agents("ws-1"), [agent]);
    qc.setQueryData(
      agentComposioToolkitAllowlistKeys.detail("agent-1"),
      { agent_id: "agent-1", toolkit_slugs: ["notion"] },
    );
  });

  afterEach(() => {
    qc.clear();
    vi.restoreAllMocks();
  });

  it("sends an intentional complete replacement and keeps raw slugs out of Agent cache", async () => {
    const { result } = renderHook(
      () => useUpdateAgentAllowlist("agent-1"),
      { wrapper: createWrapper(qc) },
    );

    await act(async () => {
      await result.current.mutateAsync(["notion", "slack"]);
    });

    expect(update).toHaveBeenCalledWith("agent-1", {
      intent: "replace",
      toolkit_slugs: ["notion", "slack"],
    });
    expect(
      qc.getQueryData(
        agentComposioToolkitAllowlistKeys.detail("agent-1"),
      ),
    ).toEqual({
      agent_id: "agent-1",
      toolkit_slugs: ["notion", "slack"],
    });
    const cachedAgent = qc.getQueryData<Agent[]>(
      workspaceKeys.agents("ws-1"),
    )?.[0];
    expect(cachedAgent).not.toHaveProperty("composio_toolkit_allowlist");
    expect(cachedAgent?.composio_toolkit_allowlist_count).toBe(2);
    expect(cachedAgent?.composio_toolkit_allowlist_redacted).toBe(true);
  });

  it("maps an empty desired list to explicit clear intent", async () => {
    const { result } = renderHook(
      () => useUpdateAgentAllowlist("agent-1"),
      { wrapper: createWrapper(qc) },
    );

    await act(async () => {
      await result.current.mutateAsync([]);
    });

    expect(update).toHaveBeenCalledWith("agent-1", { intent: "clear" });
    const cachedAgent = qc.getQueryData<Agent[]>(
      workspaceKeys.agents("ws-1"),
    )?.[0];
    expect(cachedAgent?.composio_toolkit_allowlist_count).toBe(0);
    expect(cachedAgent?.composio_toolkit_allowlist_redacted).toBe(false);
  });

  it("fences an active stale observer until a forced audited reveal installs committed state B", async () => {
    let resolveReveal:
      | ((value: {
          agent_id: string;
          toolkit_slugs: string[];
        }) => void)
      | undefined;
    reveal.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveReveal = resolve;
        }),
    );
    update.mockRejectedValueOnce(
      new CommittedResponseUnreadableError(
        "PUT /api/agents/:id/composio-toolkit-allowlist",
      ),
    );
    qc.setQueryData(
      agentComposioToolkitAllowlistKeys.detail("agent-2"),
      { agent_id: "agent-2", toolkit_slugs: ["github"] },
    );
    const { result, rerender } = renderHook(
      ({ targetAgentId }) => {
        const update = useUpdateAgentAllowlist(targetAgentId);
        const allowlist = useQuery({
          ...agentComposioToolkitAllowlistOptions(targetAgentId),
          enabled: !update.isCommitUncertain,
        });
        return { allowlist, update };
      },
      {
        initialProps: { targetAgentId: "agent-1" },
        wrapper: createWrapper(qc),
      },
    );

    expect(result.current.allowlist.data?.toolkit_slugs).toEqual([
      "notion",
    ]);
    act(() => {
      result.current.update.mutate(["notion", "slack"]);
    });

    await waitFor(() => expect(reveal).toHaveBeenCalledTimes(1));
    // QueryObserver still holds stale A while the audited GET is pending.
    // The explicit per-agent fence is therefore the interaction boundary.
    expect(result.current.allowlist.data?.toolkit_slugs).toEqual([
      "notion",
      "slack",
    ]);
    expect(result.current.update.isCommitUncertain).toBe(true);
    expect(
      result.current.update.allowlistRecoveryStatus,
    ).toBe("recovering");

    // The fence belongs only to A and survives navigation. B remains usable;
    // returning to A observes the same fence while its audited GET is pending.
    rerender({ targetAgentId: "agent-2" });
    expect(result.current.update.isCommitUncertain).toBe(false);
    expect(result.current.allowlist.data?.toolkit_slugs).toEqual([
      "github",
    ]);
    rerender({ targetAgentId: "agent-1" });
    expect(result.current.update.isCommitUncertain).toBe(true);

    act(() => {
      resolveReveal?.({
        agent_id: "agent-1",
        toolkit_slugs: ["slack"],
      });
    });
    await waitFor(() => {
      expect(result.current.allowlist.data?.toolkit_slugs).toEqual([
        "slack",
      ]);
      expect(result.current.update.isCommitUncertain).toBe(false);
    });
    expect(reveal).toHaveBeenCalledTimes(1);
  });

  it("keeps the observer fenced after reveal failure and unlocks only after an explicit successful retry", async () => {
    qc.setDefaultOptions({
      queries: { retry: 3 },
      mutations: { retry: false },
    });
    reveal.mockRejectedValueOnce(new Error("audited reveal failed"));
    update.mockRejectedValueOnce(
      new CommittedResponseUnreadableError(
        "PUT /api/agents/:id/composio-toolkit-allowlist",
      ),
    );
    const useObservedAllowlist = () => {
      const update = useUpdateAgentAllowlist("agent-1");
      const allowlist = useQuery({
        ...agentComposioToolkitAllowlistOptions("agent-1"),
        enabled: !update.isCommitUncertain,
      });
      return { allowlist, update };
    };
    const mounted = renderHook(useObservedAllowlist, {
      wrapper: createWrapper(qc),
    });

    act(() => {
      mounted.result.current.update.mutate(["slack"]);
    });
    await waitFor(() =>
      expect(
        mounted.result.current.update.allowlistRecoveryStatus,
      ).toBe("failed"),
    );
    expect(mounted.result.current.update.isCommitUncertain).toBe(true);
    expect(mounted.result.current.allowlist.data?.toolkit_slugs).toEqual([
      "slack",
    ]);
    expect(reveal).toHaveBeenCalledTimes(1);

    mounted.unmount();
    const remounted = renderHook(useObservedAllowlist, {
      wrapper: createWrapper(qc),
    });
    expect(
      remounted.result.current.update.allowlistRecoveryStatus,
    ).toBe("failed");
    expect(remounted.result.current.update.isCommitUncertain).toBe(true);
    expect(
      remounted.result.current.allowlist.data?.toolkit_slugs,
    ).toEqual(["slack"]);
    // A disabled raw observer cannot bypass the fence on remount or focus.
    act(() => {
      window.dispatchEvent(new Event("focus"));
    });
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(reveal).toHaveBeenCalledTimes(1);

    reveal.mockResolvedValue({
      agent_id: "agent-1",
      toolkit_slugs: ["github"],
    });
    await act(async () => {
      await expect(
        remounted.result.current.update.retryCommitUncertainReveal(),
      ).resolves.toBe(true);
    });
    await waitFor(() => {
      expect(
        remounted.result.current.allowlist.data?.toolkit_slugs,
      ).toEqual(["github"]);
      expect(
        remounted.result.current.update.isCommitUncertain,
      ).toBe(false);
    });
    expect(reveal).toHaveBeenCalledTimes(2);
  });

  it("settles slow A1 then B1 then A C1 under an immutable global scope", async () => {
    let resolveA1:
      | ((value: {
          agent_id: string;
          toolkit_count: number;
        }) => void)
      | undefined;
    update
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveA1 = resolve;
          }),
      )
      .mockResolvedValue({
        agent_id: "agent",
        toolkit_count: 1,
      });

    const { result, rerender } = renderHook(
      ({ targetAgentId }) => useUpdateAgentAllowlist(targetAgentId),
      {
        initialProps: { targetAgentId: "agent-1" },
        wrapper: createWrapper(qc),
      },
    );

    act(() => {
      result.current.mutate(["slack"]);
    });
    await waitFor(() => expect(update).toHaveBeenCalledTimes(1));

    rerender({ targetAgentId: "agent-2" });
    act(() => {
      result.current.mutate(["github"]);
    });
    rerender({ targetAgentId: "agent-1" });
    act(() => {
      result.current.mutate(["notion"]);
    });

    // B1 and C1 are queued behind slow A1, including across agent rerenders.
    expect(update).toHaveBeenCalledTimes(1);
    act(() => {
      resolveA1?.({ agent_id: "agent-1", toolkit_count: 1 });
    });

    await waitFor(() => expect(update).toHaveBeenCalledTimes(3));
    await waitFor(() => expect(result.current.isPending).toBe(false));
    expect(
      update.mock.calls.map(([targetAgentId, body]) => [
        targetAgentId,
        body,
      ]),
    ).toEqual([
      [
        "agent-1",
        { intent: "replace", toolkit_slugs: ["slack"] },
      ],
      [
        "agent-2",
        { intent: "replace", toolkit_slugs: ["github"] },
      ],
      [
        "agent-1",
        { intent: "replace", toolkit_slugs: ["notion"] },
      ],
    ]);
  });

  it("keeps an in-flight agent A rollback and invalidation isolated after rerendering for agent B", async () => {
    let rejectAgentA: ((error: Error) => void) | undefined;
    update.mockImplementationOnce(
      () =>
        new Promise((_resolve, reject) => {
          rejectAgentA = reject;
        }),
    );
    qc.setQueryData(
      agentComposioToolkitAllowlistKeys.detail("agent-2"),
      { agent_id: "agent-2", toolkit_slugs: ["github"] },
    );

    const { result, rerender } = renderHook(
      ({ targetAgentId }) => useUpdateAgentAllowlist(targetAgentId),
      {
        initialProps: { targetAgentId: "agent-1" },
        wrapper: createWrapper(qc),
      },
    );

    act(() => {
      result.current.mutate(["slack"]);
    });
    await waitFor(() => expect(update).toHaveBeenCalledWith("agent-1", {
      intent: "replace",
      toolkit_slugs: ["slack"],
    }));

    rerender({ targetAgentId: "agent-2" });
    act(() => {
      rejectAgentA?.(new Error("agent A request failed"));
    });
    await waitFor(() => expect(result.current.isError).toBe(true));

    expect(
      qc.getQueryData(
        agentComposioToolkitAllowlistKeys.detail("agent-1"),
      ),
    ).toEqual({
      agent_id: "agent-1",
      toolkit_slugs: ["notion"],
    });
    expect(
      qc.getQueryData(
        agentComposioToolkitAllowlistKeys.detail("agent-2"),
      ),
    ).toEqual({
      agent_id: "agent-2",
      toolkit_slugs: ["github"],
    });
  });

  it("does not let an older same-agent failure roll back a newer generation", async () => {
    let rejectFirst: ((error: Error) => void) | undefined;
    update
      .mockImplementationOnce(
        () =>
          new Promise((_resolve, reject) => {
            rejectFirst = reject;
          }),
      )
      .mockResolvedValueOnce({
        agent_id: "agent-1",
        toolkit_count: 1,
      });
    const { result } = renderHook(
      () => useUpdateAgentAllowlist("agent-1"),
      { wrapper: createWrapper(qc) },
    );

    act(() => {
      result.current.mutate(["slack"]);
      result.current.mutate(["github"]);
    });
    // Same-agent full-list writes are serialized, so the newer request cannot
    // race ahead and then be overwritten at the server by this older one.
    await waitFor(() => expect(update).toHaveBeenCalledTimes(1));
    act(() => {
      rejectFirst?.(new Error("older request failed"));
    });
    await waitFor(() => expect(update).toHaveBeenCalledTimes(2));
    await waitFor(() =>
      expect(
        qc.getQueryData(
          agentComposioToolkitAllowlistKeys.detail("agent-1"),
        ),
      ).toEqual({
        agent_id: "agent-1",
        toolkit_slugs: ["github"],
      }),
    );
  });

  it("does not let a superseded onMutate clobber newer optimism when query cancellation resolves out of order", async () => {
    const releaseFirstCancellation: Array<() => void> = [];
    let cancelCalls = 0;
    vi.spyOn(qc, "cancelQueries").mockImplementation(() => {
      cancelCalls += 1;
      if (cancelCalls <= 2) {
        return new Promise<void>((resolve) => {
          releaseFirstCancellation.push(resolve);
        });
      }
      return Promise.resolve();
    });

    const { result } = renderHook(
      () => useUpdateAgentAllowlist("agent-1"),
      { wrapper: createWrapper(qc) },
    );

    act(() => {
      result.current.mutate(["slack"]);
    });
    await waitFor(() => expect(cancelCalls).toBe(2));

    act(() => {
      result.current.mutate(["github"]);
    });
    await waitFor(() =>
      expect(
        qc.getQueryData(
          agentComposioToolkitAllowlistKeys.detail("agent-1"),
        ),
      ).toEqual({
        agent_id: "agent-1",
        toolkit_slugs: ["github"],
      }),
    );

    // The first mutation's delayed onMutate now resumes. It must not restore
    // its older ["slack"] optimism. Network writes remain invocation-ordered
    // by the per-agent mutation scope: slack first, github last.
    act(() => {
      for (const release of releaseFirstCancellation) release();
    });
    await waitFor(() => expect(update).toHaveBeenCalledTimes(2));
    expect(update.mock.calls.map(([targetAgentId, body]) => [
      targetAgentId,
      body,
    ])).toEqual([
      [
        "agent-1",
        { intent: "replace", toolkit_slugs: ["slack"] },
      ],
      [
        "agent-1",
        { intent: "replace", toolkit_slugs: ["github"] },
      ],
    ]);
    expect(
      qc.getQueryData(
        agentComposioToolkitAllowlistKeys.detail("agent-1"),
      ),
    ).toEqual({
      agent_id: "agent-1",
      toolkit_slugs: ["github"],
    });
  });
});
