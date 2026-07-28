/**
 * @vitest-environment jsdom
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import type { Agent } from "../types";
import { workspaceKeys } from "../workspace/queries";
import {
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

  beforeEach(() => {
    qc = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    });
    update.mockReset();
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
});
