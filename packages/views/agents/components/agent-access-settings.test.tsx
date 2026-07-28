// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import type { Agent } from "@multica/core/types";

const accessProps = vi.hoisted(() => ({
  current: null as null | { hasComposioAllowlist?: boolean },
}));

vi.mock("../../settings/components/settings-layout", () => ({
  SettingsSection: ({ children }: { children: ReactNode }) => <>{children}</>,
  SettingsCard: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

vi.mock("./inspector/access-picker", () => ({
  AccessPicker: (props: { hasComposioAllowlist?: boolean }) => {
    accessProps.current = props;
    return <div data-testid="access-picker" />;
  },
}));

vi.mock("../../i18n", () => ({
  useT: () => ({
    t: () => "translated",
  }),
}));

import { AgentAccessSettings } from "./agent-access-settings";

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
  owner_id: "user-1",
  permission_mode: "private",
  invocation_targets: [],
  visibility: "private",
  status: "idle",
  max_concurrent_tasks: 1,
  model: "",
  skills: [],
  created_at: "2026-07-28T00:00:00Z",
  updated_at: "2026-07-28T00:00:00Z",
  archived_at: null,
  archived_by: null,
};

describe("AgentAccessSettings", () => {
  it("derives Composio access warnings from value-free count metadata", () => {
    render(
      <AgentAccessSettings
        agent={{
          ...baseAgent,
          composio_toolkit_allowlist_count: 2,
          composio_toolkit_allowlist_redacted: true,
        }}
        members={[]}
        currentUserId="user-1"
        onUpdate={async () => {}}
      />,
    );

    expect(screen.getByTestId("access-picker")).toBeTruthy();
    expect(accessProps.current?.hasComposioAllowlist).toBe(true);
  });

  it("treats a missing/zero count as no configured allowlist", () => {
    render(
      <AgentAccessSettings
        agent={{ ...baseAgent, composio_toolkit_allowlist_count: 0 }}
        members={[]}
        currentUserId="user-1"
        onUpdate={async () => {}}
      />,
    );

    expect(accessProps.current?.hasComposioAllowlist).toBe(false);
  });
});
