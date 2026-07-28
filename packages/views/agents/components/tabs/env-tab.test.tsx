// @vitest-environment jsdom

import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import type {
  Agent,
  AgentEnvResponse,
  AgentEnvUpdateResponse,
} from "@multica/core/types";
import enCommon from "../../../locales/en/common.json";
import enAgents from "../../../locales/en/agents.json";

vi.mock("@multica/core/api", () => ({
  CommittedResponseUnreadableError: class CommittedResponseUnreadableError extends Error {
    readonly committed = true;
  },
  SecretResponseUnreadableError: class SecretResponseUnreadableError extends Error {},
  api: {
    getAgentEnv: vi.fn(),
    updateAgentEnv: vi.fn(),
  },
}));

vi.mock("sonner", () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}));

import {
  api,
  CommittedResponseUnreadableError,
  SecretResponseUnreadableError,
} from "@multica/core/api";
import { EnvTab, reconcileEnvUpdate } from "./env-tab";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };

function agent(id: string, count = 1): Agent {
  return {
    id,
    workspace_id: "ws-1",
    runtime_id: "runtime-1",
    name: `Agent ${id}`,
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    custom_env_key_count: count,
    has_custom_env: count > 0,
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
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function renderEnvTab(
  currentAgent: Agent,
  onSaved = vi.fn(),
) {
  const renderResult = render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <EnvTab agent={currentAgent} onSaved={onSaved} />
    </I18nProvider>,
  );
  return {
    ...renderResult,
    rerenderAgent(nextAgent: Agent) {
      renderResult.rerender(
        <I18nProvider locale="en" resources={TEST_RESOURCES}>
          <EnvTab agent={nextAgent} onSaved={onSaved} />
        </I18nProvider>,
      );
    },
  };
}

describe("reconcileEnvUpdate", () => {
  it("uses the value-free key confirmation without losing submitted values", () => {
    const secret = "sentinel-env-ui-secret";
    const result = reconcileEnvUpdate(
      {
        PRESERVED: "****",
        ROTATED: secret,
        PHANTOM: "****",
      },
      {
        PRESERVED: "existing-private-value",
        ROTATED: "old-private-value",
      },
      {
        agent_id: "agent-1",
        custom_env: {
          PRESERVED: "****",
          ROTATED: "****",
        },
        has_custom_env: true,
        custom_env_key_count: 2,
        custom_env_keys: ["PRESERVED", "ROTATED"],
        added_keys: [],
        removed_keys: [],
        changed_keys: ["ROTATED"],
        preserved_keys: ["PRESERVED"],
      },
    );

    expect(result).toEqual({
      PRESERVED: "existing-private-value",
      ROTATED: secret,
    });
    expect(result).not.toHaveProperty("PHANTOM");
  });
});

describe("EnvTab agent switching", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("immediately clears revealed plaintext when the agent id changes", async () => {
    const user = userEvent.setup();
    vi.mocked(api.getAgentEnv).mockResolvedValue({
      agent_id: "agent-a",
      custom_env: { TOKEN: "agent-a-private-value" },
    });
    const view = renderEnvTab(agent("agent-a"));

    await user.click(screen.getByRole("button", { name: /reveal & edit/i }));
    expect(screen.getByDisplayValue("agent-a-private-value")).toBeInTheDocument();

    view.rerenderAgent(agent("agent-b", 2));

    expect(
      screen.queryByDisplayValue("agent-a-private-value"),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /reveal & edit/i }),
    ).toBeInTheDocument();
  });

  it("ignores a deferred reveal response from the previous agent", async () => {
    const user = userEvent.setup();
    const revealA = deferred<AgentEnvResponse>();
    vi.mocked(api.getAgentEnv).mockReturnValueOnce(revealA.promise);
    const view = renderEnvTab(agent("agent-a"));

    await user.click(screen.getByRole("button", { name: /reveal & edit/i }));
    expect(api.getAgentEnv).toHaveBeenCalledWith("agent-a");
    view.rerenderAgent(agent("agent-b"));

    await act(async () => {
      revealA.resolve({
        agent_id: "agent-a",
        custom_env: { TOKEN: "late-agent-a-secret" },
      });
      await revealA.promise;
    });

    expect(screen.queryByDisplayValue("late-agent-a-secret")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /reveal & edit/i }),
    ).toBeInTheDocument();
  });

  it("ignores a deferred save response and never applies it under the next agent", async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    vi.mocked(api.getAgentEnv).mockResolvedValueOnce({
      agent_id: "agent-a",
      custom_env: { TOKEN: "agent-a-old-value" },
    });
    const saveA = deferred<AgentEnvUpdateResponse>();
    vi.mocked(api.updateAgentEnv).mockReturnValueOnce(saveA.promise);
    const view = renderEnvTab(agent("agent-a"), onSaved);

    await user.click(screen.getByRole("button", { name: /reveal & edit/i }));
    const valueInput = screen.getByDisplayValue("agent-a-old-value");
    await user.clear(valueInput);
    await user.type(valueInput, "agent-a-rotated-value");
    await user.click(screen.getByRole("button", { name: /^save$/i }));
    expect(api.updateAgentEnv).toHaveBeenCalledWith("agent-a", {
      custom_env: { TOKEN: "agent-a-rotated-value" },
    });

    view.rerenderAgent(agent("agent-b"));
    await act(async () => {
      saveA.resolve({
        agent_id: "agent-a",
        custom_env: { TOKEN: "****" },
        has_custom_env: true,
        custom_env_key_count: 1,
        custom_env_keys: ["TOKEN"],
        added_keys: [],
        removed_keys: [],
        changed_keys: ["TOKEN"],
        preserved_keys: [],
      });
      await saveA.promise;
    });

    expect(
      screen.queryByDisplayValue("agent-a-rotated-value"),
    ).not.toBeInTheDocument();
    expect(api.updateAgentEnv).toHaveBeenCalledTimes(1);
    expect(onSaved).not.toHaveBeenCalled();
    expect(
      screen.getByRole("button", { name: /reveal & edit/i }),
    ).toBeInTheDocument();
  });

  it("keeps malformed reveal responses unrevealed", async () => {
    const user = userEvent.setup();
    vi.mocked(api.getAgentEnv).mockRejectedValueOnce(
      new SecretResponseUnreadableError("unreadable"),
    );
    renderEnvTab(agent("agent-a"));

    await user.click(screen.getByRole("button", { name: /reveal & edit/i }));

    expect(api.getAgentEnv).toHaveBeenCalledTimes(1);
    expect(
      screen.getByRole("button", { name: /reveal & edit/i }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^save$/i })).not.toBeInTheDocument();
  });

  it("never reveals a response whose agent_id does not match the request", async () => {
    const user = userEvent.setup();
    vi.mocked(api.getAgentEnv).mockResolvedValueOnce({
      agent_id: "agent-b",
      custom_env: { TOKEN: "agent-b-private-value" },
    });
    renderEnvTab(agent("agent-a"));

    await user.click(screen.getByRole("button", { name: /reveal & edit/i }));

    expect(screen.queryByDisplayValue("agent-b-private-value")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /reveal & edit/i }),
    ).toBeInTheDocument();
  });

  it("requires a fresh reveal after an unreadable committed save", async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    vi.mocked(api.getAgentEnv).mockResolvedValueOnce({
      agent_id: "agent-a",
      custom_env: { TOKEN: "agent-a-old-value" },
    });
    vi.mocked(api.updateAgentEnv).mockRejectedValueOnce(
      new CommittedResponseUnreadableError("uncertain"),
    );
    renderEnvTab(agent("agent-a"), onSaved);

    await user.click(screen.getByRole("button", { name: /reveal & edit/i }));
    const valueInput = screen.getByDisplayValue("agent-a-old-value");
    await user.clear(valueInput);
    await user.type(valueInput, "agent-a-new-value");
    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(api.updateAgentEnv).toHaveBeenCalledTimes(1);
    expect(onSaved).toHaveBeenCalledTimes(1);
    expect(screen.queryByDisplayValue("agent-a-new-value")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /reveal & edit/i }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^save$/i })).not.toBeInTheDocument();
  });

  it("does not rebaseline from a save confirmation for another agent", async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    vi.mocked(api.getAgentEnv).mockResolvedValueOnce({
      agent_id: "agent-a",
      custom_env: { TOKEN: "agent-a-old-value" },
    });
    vi.mocked(api.updateAgentEnv).mockResolvedValueOnce({
      agent_id: "agent-b",
      custom_env: { TOKEN: "****" },
      has_custom_env: true,
      custom_env_key_count: 1,
      custom_env_keys: ["TOKEN"],
      added_keys: [],
      removed_keys: [],
      changed_keys: ["TOKEN"],
      preserved_keys: [],
    });
    renderEnvTab(agent("agent-a"), onSaved);

    await user.click(screen.getByRole("button", { name: /reveal & edit/i }));
    const valueInput = screen.getByDisplayValue("agent-a-old-value");
    await user.clear(valueInput);
    await user.type(valueInput, "agent-a-new-value");
    await user.click(screen.getByRole("button", { name: /^save$/i }));

    expect(screen.queryByDisplayValue("agent-a-new-value")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /reveal & edit/i }),
    ).toBeInTheDocument();
    expect(onSaved).toHaveBeenCalledTimes(1);
  });
});
