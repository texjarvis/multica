// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent } from "@multica/core/types";
import { toast } from "sonner";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

import { RuntimeConfigTab } from "./runtime-config-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };
const secret = "sentinel-runtime-config-ui-secret";

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
  visibility: "workspace",
  permission_mode: "public_to",
  invocation_targets: [{ target_type: "workspace", target_id: null }],
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  owner_id: "user-1",
  skills: [],
  created_at: "2026-07-27T00:00:00Z",
  updated_at: "2026-07-27T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

function redactedAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    ...baseAgent,
    runtime_config: {
      mode: "gateway",
      gateway: { host: "****", token: "****" },
      _redacted: "****",
      // Defense-in-depth: even a compromised legacy client payload
      // must never be seeded into the replacement form.
      unknown_secret: secret,
    },
    has_runtime_config: true,
    runtime_config_key_count: 4,
    runtime_config_redacted: true,
    ...overrides,
  };
}

function visibleAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    ...baseAgent,
    runtime_config: {
      mode: "gateway",
      gateway: {
        host: "gateway.internal",
        token: "stored-token",
      },
    },
    has_runtime_config: true,
    runtime_config_key_count: 2,
    runtime_config_redacted: false,
    ...overrides,
  };
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function renderTab(
  onSave = vi.fn().mockResolvedValue(undefined),
  initialAgent = redactedAgent(),
) {
  const renderResult = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <RuntimeConfigTab
        agent={initialAgent}
        onSave={onSave}
        canManage
      />
    </I18nProvider>,
  );
  return {
    ...renderResult,
    rerenderAgent(nextAgent: Agent) {
      renderResult.rerender(
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <RuntimeConfigTab agent={nextAgent} onSave={onSave} canManage />
        </I18nProvider>,
      );
    },
    onSave,
  };
}

describe("RuntimeConfigTab redacted state", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("replaces from a blank authoritative config without round-tripping projection values", async () => {
    const user = userEvent.setup();
    const { onSave, container } = renderTab();

    expect(container.textContent).not.toContain(secret);
    expect(
      screen.getByText(/routing configuration is hidden/i),
    ).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: /replace routing config/i }),
    );
    expect(screen.getByRole("button", { name: "Local" })).toHaveClass(
      "bg-foreground",
    );
    await user.click(screen.getByRole("button", { name: "Gateway" }));
    await user.type(screen.getByLabelText("Host"), "fresh.internal");
    await user.type(screen.getByLabelText("Auth token"), "fresh-token");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith({
      runtime_config: {
        mode: "gateway",
        gateway: {
          host: "fresh.internal",
          token: "fresh-token",
        },
      },
      runtime_config_intent: "replace",
    });
    expect(JSON.stringify(onSave.mock.calls)).not.toContain(secret);
  });

  it("clears only after explicit confirmation", async () => {
    const user = userEvent.setup();
    const { onSave } = renderTab();

    await user.click(
      screen.getByRole("button", { name: /clear routing config/i }),
    );
    expect(
      screen.getByText(/removes the complete stored routing configuration/i),
    ).toBeInTheDocument();
    const clearButtons = screen.getAllByRole("button", {
      name: /clear routing config/i,
    });
    await user.click(clearButtons.at(-1)!);

    expect(onSave).toHaveBeenCalledWith({
      runtime_config: {},
      runtime_config_intent: "clear",
    });
  });

  it("preserves an unsaved replacement token across equivalent status refetches", async () => {
    const user = userEvent.setup();
    const view = renderTab();

    await user.click(
      screen.getByRole("button", { name: /replace routing config/i }),
    );
    await user.click(screen.getByRole("button", { name: "Gateway" }));
    await user.type(
      screen.getByLabelText("Auth token"),
      "unsaved-private-token",
    );

    view.rerenderAgent(
      redactedAgent({
        status: "working",
        updated_at: "2026-07-27T00:01:00Z",
        runtime_config: {
          mode: "gateway",
          gateway: { host: "****", token: "****" },
          _redacted: "****",
        },
      }),
    );

    expect(screen.getByLabelText("Auth token")).toHaveValue(
      "unsaved-private-token",
    );
    expect(
      screen.getByRole("button", { name: /^cancel$/i }),
    ).toBeInTheDocument();
  });

  it("preserves an ordinary unsaved edit across an equivalent status refetch", async () => {
    const user = userEvent.setup();
    const view = renderTab(
      vi.fn().mockResolvedValue(undefined),
      visibleAgent(),
    );

    const token = screen.getByLabelText("Auth token");
    await user.clear(token);
    await user.type(token, "ordinary-unsaved-token");

    view.rerenderAgent(
      visibleAgent({
        status: "working",
        updated_at: "2026-07-27T00:01:00Z",
        runtime_config: {
          mode: "gateway",
          gateway: {
            host: "gateway.internal",
            token: "stored-token",
          },
        },
      }),
    );

    expect(screen.getByLabelText("Auth token")).toHaveValue(
      "ordinary-unsaved-token",
    );
  });

  it("hard-resets replacement mode and local secrets when the agent id changes", async () => {
    const user = userEvent.setup();
    const view = renderTab();

    await user.click(
      screen.getByRole("button", { name: /replace routing config/i }),
    );
    await user.click(screen.getByRole("button", { name: "Gateway" }));
    await user.type(
      screen.getByLabelText("Auth token"),
      "agent-a-unsaved-token",
    );

    view.rerenderAgent(redactedAgent({ id: "agent-2", name: "Agent 2" }));

    expect(screen.queryByLabelText("Auth token")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain("agent-a-unsaved-token");
    expect(
      screen.getByText(/routing configuration is hidden/i),
    ).toBeInTheDocument();
  });

  it("does not apply an agent A save completion after switching to agent B", async () => {
    const user = userEvent.setup();
    const pendingSave = deferred<void>();
    const onSave = vi
      .fn()
      .mockImplementationOnce(() => pendingSave.promise)
      .mockResolvedValueOnce(undefined);
    const view = renderTab(onSave);

    await user.click(
      screen.getByRole("button", { name: /replace routing config/i }),
    );
    await user.click(screen.getByRole("button", { name: "Gateway" }));
    await user.type(screen.getByLabelText("Host"), "agent-a.internal");
    await user.type(
      screen.getByLabelText("Auth token"),
      "agent-a-exact-routing-token",
    );
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(JSON.stringify(onSave.mock.calls[0])).toContain(
      "agent-a-exact-routing-token",
    );

    view.rerenderAgent(redactedAgent({ id: "agent-2", name: "Agent 2" }));
    expect(document.body.textContent).not.toContain(
      "agent-a-exact-routing-token",
    );

    await act(async () => {
      pendingSave.resolve();
      await pendingSave.promise;
    });

    expect(document.body.textContent).not.toContain(
      "agent-a-exact-routing-token",
    );
    expect(toast.success).not.toHaveBeenCalled();

    await user.click(
      screen.getByRole("button", { name: /replace routing config/i }),
    );
    await user.click(screen.getByRole("button", { name: "Gateway" }));
    await user.type(screen.getByLabelText("Host"), "agent-b.internal");
    await user.type(
      screen.getByLabelText("Auth token"),
      "agent-b-exact-routing-token",
    );
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenLastCalledWith({
      runtime_config: {
        mode: "gateway",
        gateway: {
          host: "agent-b.internal",
          token: "agent-b-exact-routing-token",
        },
      },
      runtime_config_intent: "replace",
    });
    expect(JSON.stringify(onSave.mock.calls[1])).not.toContain(
      "agent-a-exact-routing-token",
    );
  });
});
