// @vitest-environment jsdom

import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { Agent, UpdateAgentRequest } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import {
  NavigationProvider,
  type NavigationAdapter,
} from "../../navigation";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

// The DM tests exercise the header action wiring plus the real permission
// rules (via auth + member fixtures); the tabbed body and avatar/presence
// widgets are irrelevant weight, so they're stubbed.
const updatePayloadRef = vi.hoisted(() => ({
  current: null as UpdateAgentRequest | null,
}));
vi.mock("./agent-overview-pane", () => ({
  AgentOverviewPane: ({
    agent,
    onUpdate,
  }: {
    agent: Agent;
    onUpdate: (id: string, data: UpdateAgentRequest) => Promise<void>;
  }) => (
    <div>
      agent-overview-pane
      {updatePayloadRef.current && (
        <button
          type="button"
          onClick={() => void onUpdate(agent.id, updatePayloadRef.current!)}
        >
          test-update
        </button>
      )}
    </div>
  ),
}));
vi.mock("../../common/actor-avatar", () => ({
  ActorAvatar: () => <div>actor-avatar</div>,
}));
vi.mock("./agent-presence-indicator", () => ({
  AgentPresenceIndicator: () => null,
}));

const agentsRef = vi.hoisted(() => ({ current: [] as unknown[] }));
const membersRef = vi.hoisted(() => ({ current: [] as unknown[] }));
// When set, the member query never resolves — the "membership still loading"
// window in which the DM decision is undetermined.
const membersPendingRef = vi.hoisted(() => ({ current: false }));
const currentUserRef = vi.hoisted(() => ({
  current: { id: "user-1" } as { id: string } | null,
}));
const mockToastError = vi.hoisted(() => vi.fn());
const mockModalOpen = vi.hoisted(() => vi.fn());
const mockUpdateAgent = vi.hoisted(() => vi.fn());

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));
vi.mock("@multica/core/agents", () => ({
  useWorkspacePresenceMap: () => ({ byAgent: new Map() }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  agentListOptions: (wsId: string) => ({
    queryKey: ["agents", wsId],
    queryFn: () => Promise.resolve(agentsRef.current),
  }),
  memberListOptions: (wsId: string) => ({
    queryKey: ["members", wsId],
    queryFn: () =>
      membersPendingRef.current
        ? new Promise(() => {})
        : Promise.resolve(membersRef.current),
  }),
  workspaceKeys: { agents: (wsId: string) => ["agents", wsId] },
}));
vi.mock("@multica/core/runtimes", () => ({
  runtimeListOptions: (wsId: string) => ({
    queryKey: ["runtimes", wsId],
    queryFn: () => Promise.resolve([]),
  }),
}));
vi.mock("@multica/core/auth", () => {
  type AuthState = { user: { id: string } | null };
  const state = (): AuthState => ({ user: currentUserRef.current });
  const useAuthStore = Object.assign(
    (selector?: (s: AuthState) => unknown) =>
      selector ? selector(state()) : state(),
    { getState: state },
  );
  return { useAuthStore };
});
vi.mock("@multica/core/modals", () => ({
  useModalStore: Object.assign(vi.fn(), {
    getState: () => ({ open: mockModalOpen }),
  }),
}));
vi.mock("@multica/core/paths", () => ({
  useWorkspacePaths: () => ({
    agents: () => "/acme/agents",
    chat: () => "/acme/chat",
  }),
}));
vi.mock("@multica/core/api", () => {
  class ApiError extends Error {
    status: number;
    constructor(status: number, message: string) {
      super(message);
      this.status = status;
    }
  }
  return {
    api: {
      getAgent: vi.fn(() => Promise.reject(new ApiError(404, "not found"))),
      updateAgent: mockUpdateAgent,
    },
    ApiError,
  };
});
vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: mockToastError },
}));

import { AgentDetailPage } from "./agent-detail-page";

const baseAgent: Agent = {
  id: "agent-1",
  workspace_id: "ws-1",
  runtime_id: "runtime-1",
  name: "Lambda",
  description: "",
  instructions: "",
  avatar_url: null,
  runtime_mode: "local",
  runtime_config: {},
  custom_args: [],
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-2",
  skills: [],
  created_at: "2026-05-28T00:00:00Z",
  updated_at: "2026-05-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const push = vi.fn();
  const navigation: NavigationAdapter = {
    push,
    replace: vi.fn(),
    back: vi.fn(),
    pathname: "/acme/agents/agent-1",
    searchParams: new URLSearchParams(),
    getShareableUrl: (path) => path,
  };
  render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <NavigationProvider value={navigation}>
        <QueryClientProvider client={queryClient}>
          <AgentDetailPage agentId="agent-1" />
        </QueryClientProvider>
      </NavigationProvider>
    </I18nProvider>,
  );
  return { push, queryClient };
}

beforeEach(() => {
  vi.clearAllMocks();
  currentUserRef.current = { id: "user-1" };
  membersRef.current = [{ user_id: "user-1", role: "member" }];
  membersPendingRef.current = false;
  agentsRef.current = [baseAgent];
  updatePayloadRef.current = null;
  mockUpdateAgent.mockResolvedValue(baseAgent);
});

describe("AgentDetailPage DM button", () => {
  it("navigates to the chat deep link when the user can chat with the agent", async () => {
    const { push } = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "DM" }));
    expect(push).toHaveBeenCalledWith("/acme/chat?agent=agent-1");
    expect(mockToastError).not.toHaveBeenCalled();
  });

  it("shows a toast instead of navigating when the user lacks chat access", async () => {
    // Post-MUL-3963 a workspace admin can VIEW another member's private agent
    // but can no longer invoke (chat with) it — the exact case where the DM
    // button must explain itself rather than navigate.
    agentsRef.current = [
      { ...baseAgent, permission_mode: "private", invocation_targets: [] },
    ];
    membersRef.current = [{ user_id: "user-1", role: "admin" }];
    const { push } = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "DM" }));
    expect(mockToastError).toHaveBeenCalledWith(
      "You don't have access to chat with this agent.",
    );
    expect(push).not.toHaveBeenCalled();
  });

  it("disables DM while membership is resolving instead of toasting a false deny", async () => {
    // Review P2: a pending member query collapses role to null, which the
    // rules read as not_member — a legitimate public_to+workspace member
    // would get a wrong "no access" toast. Undetermined must disable, not deny.
    membersPendingRef.current = true;
    const { push } = renderPage();
    const dm = await screen.findByRole("button", { name: "DM" });
    expect(dm).toBeDisabled();
    fireEvent.click(dm);
    expect(mockToastError).not.toHaveBeenCalled();
    expect(push).not.toHaveBeenCalled();
  });

  it("hides the DM button on an archived agent", async () => {
    agentsRef.current = [
      { ...baseAgent, archived_at: "2026-06-01T00:00:00Z" },
    ];
    renderPage();
    // The archived banner is the signal the page has settled past loading.
    await screen.findByText(/This agent is archived/);
    expect(screen.queryByRole("button", { name: "DM" })).not.toBeInTheDocument();
  });
});

describe("AgentDetailPage update cache boundary", () => {
  it("never copies secret-bearing request fields into the shared agent cache", async () => {
    const sentinel = "sentinel-agent-cache-secret";
    updatePayloadRef.current = {
      name: "Optimistic safe name",
      runtime_config: { token: sentinel },
      runtime_config_intent: "replace",
      custom_args: ["--token", sentinel],
      mcp_config: { Authorization: `Bearer ${sentinel}` },
      custom_args_intent: "replace",
      mcp_config_intent: "replace",
    };
    mockUpdateAgent.mockReturnValueOnce(new Promise(() => {}));
    const { queryClient } = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "test-update" }));

    const cached = queryClient.getQueryData<Agent[]>(["agents", "ws-1"]);
    expect(cached?.[0]?.name).toBe("Optimistic safe name");
    expect(JSON.stringify(cached)).not.toContain(sentinel);
    expect(cached?.[0]?.runtime_config).toEqual({});
    expect(cached?.[0]?.custom_args).toEqual([]);
    expect(cached?.[0]?.mcp_config).toBeUndefined();
    expect(cached?.[0]).not.toHaveProperty("custom_args_intent");
    expect(cached?.[0]).not.toHaveProperty("mcp_config_intent");
    expect(cached?.[0]).not.toHaveProperty("runtime_config_intent");
  });
});
