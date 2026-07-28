import { describe, expect, it } from "vitest";
import { AgentSchema } from "../data/schemas";

describe("AgentSchema invocation permissions", () => {
  it("defaults missing invocation permissions to private access", () => {
    const parsed = AgentSchema.parse({ id: "agent-1" });

    expect(parsed.permission_mode).toBe("private");
    expect(parsed.invocation_targets).toEqual([]);
  });

  it("parses public invocation grants", () => {
    const parsed = AgentSchema.parse({
      id: "agent-1",
      permission_mode: "public_to",
      invocation_targets: [
        { target_type: "workspace" },
        { target_type: "member", target_id: "member-1" },
      ],
    });

    expect(parsed.permission_mode).toBe("public_to");
    expect(parsed.invocation_targets).toEqual([
      { target_type: "workspace", target_id: null },
      { target_type: "member", target_id: "member-1" },
    ]);
  });

  it("fails closed for unknown permission values", () => {
    const parsed = AgentSchema.parse({
      id: "agent-1",
      permission_mode: "future_mode",
      invocation_targets: [{ target_type: "future_target", target_id: 123 }],
    });

    expect(parsed.permission_mode).toBe("private");
    expect(parsed.invocation_targets).toEqual([
      { target_type: "team", target_id: null },
    ]);
  });

  it("strips legacy secret-bearing and raw Composio fields", () => {
    const secret = "sentinel-mobile-agent-secret";
    const parsed = AgentSchema.parse({
      id: "agent-1",
      custom_env: { TOKEN: secret },
      custom_args: ["--token", secret],
      runtime_config: { api_key: secret },
      mcp_config: { headers: { Authorization: secret } },
      composio_toolkit_allowlist: ["notion", secret],
      composio_toolkit_allowlist_count: 2,
      composio_toolkit_allowlist_redacted: true,
      unknown_secret: secret,
    });

    expect(JSON.stringify(parsed)).not.toContain(secret);
    expect(parsed.runtime_config).toEqual({});
    expect(parsed.custom_args).toEqual([]);
    expect(parsed.mcp_config).toBeNull();
    expect(parsed).not.toHaveProperty("custom_env");
    expect(parsed).not.toHaveProperty("composio_toolkit_allowlist");
    expect(parsed.composio_toolkit_allowlist_count).toBe(2);
    expect(parsed.composio_toolkit_allowlist_redacted).toBe(true);
  });
});
