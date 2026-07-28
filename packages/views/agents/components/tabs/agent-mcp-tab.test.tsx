// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Agent } from "@multica/core/types";
import { configStore } from "@multica/core/config";
import { COMPOSIO_MCP_APPS_FLAG } from "@multica/core/feature-flags";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

const connectionsRef = vi.hoisted(() => ({
  current: [] as { toolkit_slug: string; status: string }[],
}));
const toolkitsRef = vi.hoisted(() => ({
  current: [] as { slug: string; name: string }[],
}));
const allowlistRef = vi.hoisted(() => ({ current: [] as string[] }));
const connectionStateRef = vi.hoisted(() => ({
  isLoading: false,
  isError: false,
}));
const allowlistStateRef = vi.hoisted(() => ({
  isLoading: false,
  isFetching: false,
  isError: false,
  hasData: true,
}));
const queryCallsRef = vi.hoisted(() => ({
  current: [] as { queryKey: unknown[]; enabled?: boolean }[],
}));
const mutateSpy = vi.hoisted(() => vi.fn());
const retryRevealSpy = vi.hoisted(() => vi.fn());
const allowlistRefetchSpy = vi.hoisted(() => vi.fn());
const isPendingRef = vi.hoisted(() => ({ current: false }));
const recoveryStateRef = vi.hoisted(() => ({
  status: "idle" as "idle" | "recovering" | "failed",
  isCommitUncertain: false,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (opts: { queryKey: unknown[]; enabled?: boolean }) => {
    queryCallsRef.current.push(opts);
    const key = JSON.stringify(opts.queryKey);
    if (key.includes("composio-toolkit-allowlist")) {
      return {
        data:
          allowlistStateRef.isLoading ||
          !allowlistStateRef.hasData
            ? undefined
            : { agent_id: "agent-1", toolkit_slugs: allowlistRef.current },
        isLoading: allowlistStateRef.isLoading,
        isFetching: allowlistStateRef.isFetching,
        isError: allowlistStateRef.isError,
        refetch: allowlistRefetchSpy,
      };
    }
    if (connectionStateRef.isLoading) {
      return { data: undefined, isLoading: true, isError: false };
    }
    if (connectionStateRef.isError) {
      return { data: undefined, isLoading: false, isError: true };
    }
    if (key.includes("connections")) {
      return {
        data: connectionsRef.current,
        isLoading: false,
        isError: false,
      };
    }
    if (key.includes("toolkits")) {
      return { data: toolkitsRef.current, isLoading: false, isError: false };
    }
    return { data: undefined, isLoading: false, isError: false };
  },
  queryOptions: <T,>(opts: T) => opts,
}));

vi.mock("@multica/core/composio", () => ({
  composioConnectionsOptions: () => ({
    queryKey: ["composio", "connections"],
  }),
  composioToolkitsOptions: () => ({ queryKey: ["composio", "toolkits"] }),
}));

vi.mock("@multica/core/agents", () => ({
  agentComposioToolkitAllowlistOptions: (agentId: string) => ({
    queryKey: ["agents", agentId, "composio-toolkit-allowlist"],
  }),
  useUpdateAgentAllowlist: () => ({
    mutate: mutateSpy,
    isPending: isPendingRef.current,
    allowlistRecoveryStatus: recoveryStateRef.status,
    isCommitUncertain: recoveryStateRef.isCommitUncertain,
    retryCommitUncertainReveal: retryRevealSpy,
  }),
}));

vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({ settings: () => "/ws/settings" }),
}));

vi.mock("../../../navigation", () => ({
  AppLink: ({ href, children }: { href: string; children: ReactNode }) => (
    <a href={href} data-testid="app-link">
      {children}
    </a>
  ),
}));

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

import { AgentMcpTab } from "./agent-mcp-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Agent",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  composio_toolkit_allowlist_count: 1,
  composio_toolkit_allowlist_redacted: true,
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-06-30T00:00:00Z",
  updated_at: "2026-06-30T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function renderTab(overrides: Partial<Agent> = {}) {
  const agent = { ...baseAgent, ...overrides };
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <AgentMcpTab agent={agent} />
    </I18nProvider>,
  );
}

describe("AgentMcpTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    connectionsRef.current = [
      { toolkit_slug: "notion", status: "active" },
      { toolkit_slug: "slack", status: "active" },
    ];
    toolkitsRef.current = [
      { slug: "notion", name: "Notion" },
      { slug: "slack", name: "Slack" },
    ];
    allowlistRef.current = ["notion"];
    connectionStateRef.isLoading = false;
    connectionStateRef.isError = false;
    allowlistStateRef.isLoading = false;
    allowlistStateRef.isFetching = false;
    allowlistStateRef.isError = false;
    allowlistStateRef.hasData = true;
    isPendingRef.current = false;
    recoveryStateRef.status = "idle";
    recoveryStateRef.isCommitUncertain = false;
    retryRevealSpy.mockResolvedValue(true);
    allowlistRefetchSpy.mockResolvedValue(undefined);
    queryCallsRef.current = [];
    configStore.getState().setFeatureFlags({
      [COMPOSIO_MCP_APPS_FLAG]: true,
    });
  });

  it("renders nothing and disables all Composio queries when the feature flag is off", () => {
    configStore.getState().setFeatureFlags({
      [COMPOSIO_MCP_APPS_FLAG]: false,
    });

    const { container } = renderTab();

    expect(container.firstChild).toBeNull();
    expect(queryCallsRef.current).toHaveLength(3);
    expect(
      queryCallsRef.current.every((call) => call.enabled === false),
    ).toBe(true);
  });

  it("uses the dedicated reveal despite a redacted generic Agent response", () => {
    renderTab();

    expect(
      screen.queryByText(/hidden from your view/i),
    ).toBeNull();
    expect(
      screen.getByLabelText(/Allow Notion for this agent/i).getAttribute(
        "aria-checked",
      ),
    ).toBe("true");
    expect(
      screen.getByLabelText(/Allow Slack for this agent/i).getAttribute(
        "aria-checked",
      ),
    ).toBe("false");
  });

  it("checking a toolkit writes the augmented revealed allowlist", async () => {
    const user = userEvent.setup();
    allowlistRef.current = [];
    renderTab();

    await user.click(screen.getByLabelText(/Allow Notion for this agent/i));

    expect(mutateSpy).toHaveBeenCalledTimes(1);
    expect(mutateSpy.mock.calls[0]?.[0]).toEqual(["notion"]);
  });

  it("unchecking a toolkit removes only that slug", async () => {
    const user = userEvent.setup();
    allowlistRef.current = ["notion", "slack"];
    renderTab();

    await user.click(screen.getByLabelText(/Allow Notion for this agent/i));

    expect(mutateSpy).toHaveBeenCalledTimes(1);
    expect(mutateSpy.mock.calls[0]?.[0]).toEqual(["slack"]);
  });

  it("unchecking the final toolkit requests an explicit clear through the hook", async () => {
    const user = userEvent.setup();
    allowlistRef.current = ["notion"];
    renderTab();

    await user.click(screen.getByLabelText(/Allow Notion for this agent/i));

    expect(mutateSpy).toHaveBeenCalledTimes(1);
    expect(mutateSpy.mock.calls[0]?.[0]).toEqual([]);
  });

  it("locks the editor when the privileged reveal fails", () => {
    allowlistStateRef.isError = true;
    renderTab();

    expect(screen.getByText(/hidden from your view/i)).toBeTruthy();
    expect(screen.queryByLabelText(/Allow Notion for this agent/i)).toBeNull();
    expect(mutateSpy).not.toHaveBeenCalled();
  });

  it("does not render empty unchecked controls while the reveal is loading", () => {
    allowlistStateRef.isLoading = true;
    renderTab();

    expect(screen.getByText(/Loading your connections/i)).toBeTruthy();
    expect(screen.queryByLabelText(/Allow Notion for this agent/i)).toBeNull();
  });

  it("locks A after an unreadable committed update, then bases the next toggle on audited state B", async () => {
    const user = userEvent.setup();
    const { rerender } = renderTab();

    // An active QueryObserver can retain stale/optimistic state A after a
    // malformed 2xx confirmation. The explicit hook fence, rather than cache
    // deletion, must make every full-list control non-interactive.
    recoveryStateRef.status = "recovering";
    recoveryStateRef.isCommitUncertain = true;
    allowlistStateRef.isFetching = true;
    rerender(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AgentMcpTab agent={baseAgent} />
      </I18nProvider>,
    );
    expect(screen.getByText(/Loading your connections/i)).toBeTruthy();
    expect(screen.queryByLabelText(/Allow Notion for this agent/i)).toBeNull();
    expect(mutateSpy).not.toHaveBeenCalled();
    expect(
      queryCallsRef.current
        .filter((call) =>
          JSON.stringify(call.queryKey).includes(
            "composio-toolkit-allowlist",
          ),
        )
        .at(-1)?.enabled,
    ).toBe(false);

    // A successful audited reveal establishes state B. The next toggle must
    // augment B, not resurrect the stale A list from before the uncertain
    // commit.
    allowlistRef.current = ["slack"];
    allowlistStateRef.isFetching = false;
    recoveryStateRef.status = "idle";
    recoveryStateRef.isCommitUncertain = false;
    rerender(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AgentMcpTab agent={baseAgent} />
      </I18nProvider>,
    );
    await user.click(screen.getByLabelText(/Allow Notion for this agent/i));
    expect(mutateSpy).toHaveBeenLastCalledWith(
      ["slack", "notion"],
      expect.any(Object),
    );
  });

  it("keeps stale A locked across a failed recovery remount and exposes only the audited retry", async () => {
    const user = userEvent.setup();
    recoveryStateRef.status = "failed";
    recoveryStateRef.isCommitUncertain = true;
    allowlistStateRef.isError = true;
    allowlistRef.current = ["notion"];

    const first = renderTab();
    expect(screen.getByText(/hidden from your view/i)).toBeTruthy();
    expect(screen.queryByLabelText(/Allow Notion for this agent/i)).toBeNull();
    expect(mutateSpy).not.toHaveBeenCalled();
    first.unmount();

    renderTab();
    expect(screen.queryByLabelText(/Allow Notion for this agent/i)).toBeNull();
    expect(
      queryCallsRef.current
        .filter((call) =>
          JSON.stringify(call.queryKey).includes(
            "composio-toolkit-allowlist",
          ),
        )
        .every((call) => call.enabled === false),
    ).toBe(true);

    await user.click(
      screen.getByRole("button", { name: /Retry loading apps/i }),
    );
    expect(retryRevealSpy).toHaveBeenCalledTimes(1);
    expect(allowlistRefetchSpy).not.toHaveBeenCalled();
    expect(mutateSpy).not.toHaveBeenCalled();
  });

  it("only offers active connections", () => {
    connectionsRef.current = [
      { toolkit_slug: "notion", status: "active" },
      { toolkit_slug: "github", status: "expired" },
    ];
    renderTab();

    expect(screen.getByLabelText(/Allow Notion for this agent/i)).toBeTruthy();
    expect(screen.queryByLabelText(/Allow github for this agent/i)).toBeNull();
  });

  it("shows an empty state with a Settings link when there are no active connections", () => {
    connectionsRef.current = [];
    allowlistRef.current = [];
    renderTab();

    expect(screen.getByText(/No connected apps yet/i)).toBeTruthy();
    expect(screen.getByTestId("app-link").getAttribute("href")).toBe(
      "/ws/settings?tab=integrations",
    );
  });

  it("shows the strong workspace warning for a public_to-workspace agent", () => {
    allowlistRef.current = [];
    renderTab({
      permission_mode: "public_to",
      invocation_targets: [{ target_type: "workspace", target_id: null }],
    });

    expect(
      screen.getByText(/any workspace member may use these Composio apps/i),
    ).toBeTruthy();
  });

  it("shows the generic shared warning for a public_to-member agent", () => {
    allowlistRef.current = [];
    renderTab({
      visibility: "private",
      permission_mode: "public_to",
      invocation_targets: [{ target_type: "member", target_id: "user-2" }],
    });

    expect(screen.getByText(/This agent is shared\./i)).toBeTruthy();
    expect(
      screen.queryByText(/any workspace member may use these Composio apps/i),
    ).toBeNull();
  });

  it("shows no sharing warning for a private agent", () => {
    renderTab({
      visibility: "private",
      permission_mode: "private",
      invocation_targets: [],
    });

    expect(screen.queryByText(/This agent is shared\./i)).toBeNull();
    expect(
      screen.queryByText(/any workspace member may use these Composio apps/i),
    ).toBeNull();
  });

  it("does not crash when invocation_targets is undefined", () => {
    expect(() =>
      renderTab({
        permission_mode: "public_to",
        invocation_targets:
          undefined as unknown as Agent["invocation_targets"],
      }),
    ).not.toThrow();
    expect(
      screen.queryByText(/any workspace member may use these Composio apps/i),
    ).toBeNull();
  });
});
