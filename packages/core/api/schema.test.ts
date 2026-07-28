import { afterEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { ApiClient, CommittedResponseUnreadableError } from "./client";
import { noopLogger, type Logger } from "../logger";
import {
  parseSecretWithFallback,
  parseWithFallback,
  SecretResponseUnreadableError,
  setSchemaLogger,
} from "./schema";

// Helper: stub fetch with a single JSON response. Status defaults to 200.
function stubFetchJson(body: unknown, status = 200) {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue(
      new Response(typeof body === "string" ? body : JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  setSchemaLogger(noopLogger);
});

function recordingSchemaLogger(): {
  logger: Logger;
  warn: ReturnType<typeof vi.fn>;
} {
  const warn = vi.fn();
  return {
    warn,
    logger: {
      debug: vi.fn(),
      info: vi.fn(),
      warn,
      error: vi.fn(),
    },
  };
}

describe("secret-safe API response logging", () => {
  const secret = "sentinel-malformed-env-response-secret";

  it("never logs malformed GET env response values or key names", async () => {
    const { logger, warn } = recordingSchemaLogger();
    setSchemaLogger(logger);
    stubFetchJson({
      agent_id: 123,
      custom_env: { ["TOKEN_" + secret]: secret },
    });

    const client = new ApiClient("https://api.example.test");
    await expect(client.getAgentEnv("agent-1")).rejects.toBeInstanceOf(
      SecretResponseUnreadableError,
    );

    const logged = JSON.stringify(warn.mock.calls);
    expect(logged).not.toContain(secret);
    expect(logged).not.toContain("TOKEN_");
    expect(logged).not.toContain("received");
    expect(logged).toContain("GET /api/agents/:id/env");
    expect(logged).toContain("issue_count");
  });

  it("never logs malformed PUT env response values or key names", async () => {
    const { logger, warn } = recordingSchemaLogger();
    setSchemaLogger(logger);
    stubFetchJson({
      agent_id: 123,
      custom_env_key_count: "wrong-type",
      custom_env: { ["AUTHORIZATION_" + secret]: secret },
    });

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.updateAgentEnv("agent-1", {
        custom_env: { TOKEN: "request-value" },
      }),
    ).rejects.toMatchObject({
      name: "CommittedResponseUnreadableError",
      committed: true,
      endpoint: "PUT /api/agents/:id/env",
    });

    const logged = JSON.stringify(warn.mock.calls);
    expect(logged).not.toContain(secret);
    expect(logged).not.toContain("AUTHORIZATION_");
    expect(logged).not.toContain("received");
    expect(logged).not.toContain("wrong-type");
    expect(logged).toContain("PUT /api/agents/:id/env");
  });

  it("accepts an old-server plaintext PUT response without exposing or logging values", async () => {
    const legacySecret = "sentinel-old-server-env-value";
    const { logger, warn } = recordingSchemaLogger();
    setSchemaLogger(logger);
    stubFetchJson({
      agent_id: "agent-1",
      custom_env: {
        API_TOKEN: legacySecret,
        SECONDARY: "another-old-server-secret",
      },
    });

    const client = new ApiClient("https://api.example.test");
    const result = await client.updateAgentEnv("agent-1", {
      custom_env: { API_TOKEN: "replacement-value" },
    });

    expect(result).toEqual({
      agent_id: "agent-1",
      custom_env: {
        API_TOKEN: "****",
        SECONDARY: "****",
      },
      has_custom_env: true,
      custom_env_key_count: 2,
      custom_env_keys: ["API_TOKEN", "SECONDARY"],
      added_keys: [],
      removed_keys: [],
      changed_keys: [],
      preserved_keys: [],
    });
    expect(JSON.stringify(result)).not.toContain(legacySecret);
    expect(JSON.stringify(result)).not.toContain("another-old-server-secret");
    expect(warn).not.toHaveBeenCalled();
  });

  it("rejects a missing GET map and a response for another agent", async () => {
    const client = new ApiClient("https://api.example.test");

    stubFetchJson({ agent_id: "agent-1" });
    await expect(client.getAgentEnv("agent-1")).rejects.toBeInstanceOf(
      SecretResponseUnreadableError,
    );

    stubFetchJson({
      agent_id: "agent-2",
      custom_env: { TOKEN: "other-agent-private-value" },
    });
    await expect(client.getAgentEnv("agent-1")).rejects.toBeInstanceOf(
      SecretResponseUnreadableError,
    );
  });

  it("treats incomplete, inconsistent, or cross-agent PUT confirmations as commit-uncertain", async () => {
    const client = new ApiClient("https://api.example.test");
    const expectCommittedUnreadable = async (body: unknown) => {
      stubFetchJson(body);
      await expect(
        client.updateAgentEnv("agent-1", {
          custom_env: { TOKEN: "replacement-value" },
        }),
      ).rejects.toBeInstanceOf(CommittedResponseUnreadableError);
    };

    await expectCommittedUnreadable({
      agent_id: "agent-1",
      custom_env: {},
      has_custom_env: false,
    });
    await expectCommittedUnreadable({
      agent_id: "agent-1",
      custom_env: { TOKEN: "****" },
      has_custom_env: true,
      custom_env_key_count: 0,
      custom_env_keys: ["TOKEN"],
      added_keys: [],
      removed_keys: [],
      changed_keys: [],
      preserved_keys: [],
    });
    await expectCommittedUnreadable({
      agent_id: "agent-2",
      custom_env: { TOKEN: "****" },
      has_custom_env: true,
      custom_env_key_count: 1,
      custom_env_keys: ["TOKEN"],
      added_keys: [],
      removed_keys: [],
      changed_keys: ["TOKEN"],
      preserved_keys: [],
    });
  });

  it("keeps Composio slugs behind the privileged GET and never logs malformed values", async () => {
    const secretSlug = "sentinel-private-toolkit-slug";
    const { logger, warn } = recordingSchemaLogger();
    setSchemaLogger(logger);
    stubFetchJson({
      agent_id: 123,
      toolkit_slugs: [secretSlug],
    });

    const client = new ApiClient("https://api.example.test");
    await expect(
      client.getAgentComposioToolkitAllowlist("agent-1"),
    ).rejects.toBeInstanceOf(SecretResponseUnreadableError);

    const logged = JSON.stringify(warn.mock.calls);
    expect(logged).not.toContain(secretSlug);
    expect(logged).toContain(
      "GET /api/agents/:id/composio-toolkit-allowlist",
    );
  });

  it("rejects a cross-agent Composio reveal and treats a cross-agent update confirmation as commit-uncertain", async () => {
    const client = new ApiClient("https://api.example.test");

    stubFetchJson({ agent_id: "agent-2", toolkit_slugs: ["notion"] });
    await expect(
      client.getAgentComposioToolkitAllowlist("agent-1"),
    ).rejects.toBeInstanceOf(SecretResponseUnreadableError);

    stubFetchJson({
      agent_id: "agent-2",
      toolkit_count: 1,
    });
    await expect(
      client.updateAgentComposioToolkitAllowlist("agent-1", {
        intent: "replace",
        toolkit_slugs: ["notion"],
      }),
    ).rejects.toBeInstanceOf(CommittedResponseUnreadableError);
  });

  it("sends explicit Composio replace and clear intent to the dedicated endpoint", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ agent_id: "agent-1", toolkit_count: 1 }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({ agent_id: "agent-1", toolkit_count: 0 }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        ),
      );
    vi.stubGlobal("fetch", fetchMock);
    const client = new ApiClient("https://api.example.test");

    await client.updateAgentComposioToolkitAllowlist("agent-1", {
      intent: "replace",
      toolkit_slugs: ["notion"],
    });
    await client.updateAgentComposioToolkitAllowlist("agent-1", {
      intent: "clear",
    });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[0]?.[0]).toContain(
      "/api/agents/agent-1/composio-toolkit-allowlist",
    );
    expect(
      JSON.parse(String((fetchMock.mock.calls[0]?.[1] as RequestInit).body)),
    ).toEqual({ intent: "replace", toolkit_slugs: ["notion"] });
    expect(
      JSON.parse(String((fetchMock.mock.calls[1]?.[1] as RequestInit).body)),
    ).toEqual({ intent: "clear" });
  });

  it("redacts string path segments in direct secret-safe parsing", () => {
    const { logger, warn } = recordingSchemaLogger();
    setSchemaLogger(logger);

    const fallback = { safe: true };
    expect(() =>
      parseSecretWithFallback(
        { [secret]: secret },
        z.object({ required: z.string() }),
        fallback,
        { endpoint: "GET /secret-test" },
      ),
    ).toThrow(SecretResponseUnreadableError);
    expect(JSON.stringify(warn.mock.calls)).not.toContain(secret);
  });

  it("distinguishes an unreadable committed mutation from a safe retryable failure", async () => {
    stubFetchJson({ id: 123, custom_env: { TOKEN: secret } }, 200);
    const client = new ApiClient("https://api.example.test");

    let caught: unknown;
    try {
      await client.updateAgentEnv("agent-1", {
        custom_env: { TOKEN: "replacement" },
      });
    } catch (error) {
      caught = error;
    }
    expect(caught).toBeInstanceOf(CommittedResponseUnreadableError);
    expect((caught as CommittedResponseUnreadableError).message).toContain(
      "may have saved",
    );
    expect((caught as CommittedResponseUnreadableError).message).toContain(
      "Refresh before retrying",
    );
    expect((caught as Error).message).not.toContain(secret);
  });
});

// These tests cover the five failure modes that white-screened the desktop
// app in past incidents. Non-secret reads degrade to an empty/safe shape.
// Secret-bearing reads instead throw a sanitized error so callers cannot
// confuse contract drift with a genuine empty value.
describe("ApiClient schema fallback", () => {
  describe("listTimeline", () => {
    it("falls back to an empty array when the body is null", async () => {
      stubFetchJson(null);
      const client = new ApiClient("https://api.example.test");
      const entries = await client.listTimeline("issue-1");
      expect(entries).toEqual([]);
    });

    it("falls back when the body is not an array", async () => {
      stubFetchJson({ wrong: "shape" });
      const client = new ApiClient("https://api.example.test");
      const entries = await client.listTimeline("issue-1");
      expect(entries).toEqual([]);
    });

    it("accepts a new entry type rather than crashing on enum drift", async () => {
      stubFetchJson([
        {
          type: "future_kind", // not in TS union
          id: "e-1",
          actor_type: "member",
          actor_id: "u-1",
          created_at: "2026-01-01T00:00:00Z",
        },
      ]);
      const client = new ApiClient("https://api.example.test");
      const entries = await client.listTimeline("issue-1");
      expect(entries).toHaveLength(1);
      expect(entries[0]?.type).toBe("future_kind");
    });

    // Forward-compat: when the server adds a new field to an existing
    // shape, `.loose()` lets it pass through unchanged. Without `.loose()`
    // zod 4 strips it, which would silently break a future TS type that
    // adopts the field — see schemas.ts header comment.
    it("preserves unknown fields the schema didn't list", async () => {
      stubFetchJson([
        {
          type: "comment",
          id: "e-1",
          actor_type: "member",
          actor_id: "u-1",
          created_at: "2026-01-01T00:00:00Z",
          // New server-side field not present in TimelineEntrySchema:
          future_field: { nested: "value" },
        },
      ]);
      const client = new ApiClient("https://api.example.test");
      const entries = await client.listTimeline("issue-1");
      const entry = entries[0] as unknown as Record<string, unknown>;
      expect(entry.future_field).toEqual({ nested: "value" });
    });
  });

  describe("listIssues", () => {
    it("falls back to an empty list when the response is malformed", async () => {
      // `issues` having the wrong type triggers the fallback. An object
      // with only unexpected keys would *succeed* parsing now (every
      // declared field has a default) and just pass the extras through
      // via `.loose()`, so we use a wrong-type payload here instead.
      stubFetchJson({ issues: "not-an-array", total: 0 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listIssues();
      expect(res).toEqual({ issues: [], total: 0 });
    });
  });

  describe("createIssue", () => {
    // The create modal decides whether to run its label-attach fallback by
    // reading `labels` off the parsed response, and treats a rejection as a
    // failed create (keep the draft, failure toast). So: a valid issue with
    // any labels shape resolves (labels absent → undefined, valid → Label[],
    // malformed → undefined), but a body that isn't a usable issue rejects
    // rather than fabricating a blank "success".
    const validIssue = {
      id: "issue-1",
      workspace_id: "ws-1",
      number: 1,
      identifier: "MUL-1",
      title: "Created",
      description: null,
      status: "todo",
      priority: "none",
      assignee_type: null,
      assignee_id: null,
      creator_type: "member",
      creator_id: "user-1",
      parent_issue_id: null,
      project_id: null,
      position: 0,
      stage: null,
      start_date: null,
      due_date: null,
      metadata: {},
      properties: {},
      created_at: "2025-01-01T00:00:00Z",
      updated_at: "2025-01-01T00:00:00Z",
    };
    const label = {
      id: "label-1",
      workspace_id: "ws-1",
      name: "bug",
      color: "#ef4444",
      created_at: "2025-01-01T00:00:00Z",
      updated_at: "2025-01-01T00:00:00Z",
    };

    it("keeps labels undefined when the backend omits the field (older backend)", async () => {
      stubFetchJson(validIssue, 201);
      const client = new ApiClient("https://api.example.test");
      const issue = await client.createIssue({ title: "Created" });
      expect(issue.id).toBe("issue-1");
      expect(issue.labels).toBeUndefined();
    });

    it("validates a well-formed labels array", async () => {
      stubFetchJson({ ...validIssue, labels: [label] }, 201);
      const client = new ApiClient("https://api.example.test");
      const issue = await client.createIssue({ title: "Created" });
      expect(issue.labels?.map((l) => l.id)).toEqual(["label-1"]);
    });

    it("degrades a null labels field to undefined so the client falls back", async () => {
      stubFetchJson({ ...validIssue, labels: null }, 201);
      const client = new ApiClient("https://api.example.test");
      const issue = await client.createIssue({ title: "Created" });
      // The issue itself still parses; only the malformed labels degrade.
      expect(issue.id).toBe("issue-1");
      expect(issue.labels).toBeUndefined();
    });

    it("degrades a labels array of the wrong element shape to undefined", async () => {
      stubFetchJson({ ...validIssue, labels: [{ nope: true }] }, 201);
      const client = new ApiClient("https://api.example.test");
      const issue = await client.createIssue({ title: "Created" });
      expect(issue.id).toBe("issue-1");
      expect(issue.labels).toBeUndefined();
    });

    it("rejects when the whole response body is not a usable issue (no fake success)", async () => {
      stubFetchJson({ not: "an issue" }, 201);
      const client = new ApiClient("https://api.example.test");
      await expect(client.createIssue({ title: "Created" })).rejects.toThrow();
    });

    it("rejects when the created issue has an empty id", async () => {
      stubFetchJson({ ...validIssue, id: "" }, 201);
      const client = new ApiClient("https://api.example.test");
      await expect(client.createIssue({ title: "Created" })).rejects.toThrow();
    });
  });

  describe("searchIssues", () => {
    it("falls back to an empty result when the response is malformed", async () => {
      stubFetchJson({ issues: "not-an-array", total: 0 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.searchIssues({ q: "bug" });
      expect(res).toEqual({ issues: [], total: 0 });
    });
  });

  describe("searchProjects", () => {
    it("falls back to an empty result when the response is malformed", async () => {
      stubFetchJson({ projects: "not-an-array", total: 0 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.searchProjects({ q: "roadmap" });
      expect(res).toEqual({ projects: [], total: 0 });
    });
  });

  describe("listAutopilots", () => {
    const baseAutopilot = {
      id: "ap-1",
      workspace_id: "ws-1",
      title: "Daily triage",
      description: null,
      assignee_id: "agent-1",
      status: "active",
      execution_mode: "run_only",
      issue_title_template: null,
      created_by_type: "member",
      created_by_id: "user-1",
      last_run_at: null,
      created_at: "2026-06-01T00:00:00Z",
      updated_at: "2026-06-01T00:00:00Z",
    };

    it("falls back to an empty list when the response is malformed", async () => {
      stubFetchJson({ autopilots: "not-an-array", total: 1 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listAutopilots();
      expect(res).toEqual({ autopilots: [], total: 0 });
    });

    it("accepts an old-server row without assignee_type or derived fields", async () => {
      // Pre-MUL-2429 servers omit assignee_type; servers older than the
      // list-derived-fields change omit trigger_kinds/next_run_at/
      // last_run_status. Both must parse, not fall back.
      stubFetchJson({ autopilots: [baseAutopilot], total: 1 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listAutopilots();
      expect(res.autopilots).toHaveLength(1);
      expect(res.autopilots[0]?.assignee_type).toBe("agent");
      expect(res.autopilots[0]?.trigger_kinds).toBeUndefined();
      expect(res.autopilots[0]?.last_run_status).toBeUndefined();
    });

    it("passes derived fields through and tolerates enum drift", async () => {
      stubFetchJson({
        autopilots: [
          {
            ...baseAutopilot,
            assignee_type: "squad",
            trigger_kinds: ["schedule", "some_future_kind"],
            next_run_at: "2026-06-13T09:00:00Z",
            last_run_status: "some_future_status",
          },
        ],
        total: 1,
      });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listAutopilots();
      expect(res.autopilots[0]?.trigger_kinds).toEqual([
        "schedule",
        "some_future_kind",
      ]);
      expect(res.autopilots[0]?.next_run_at).toBe("2026-06-13T09:00:00Z");
      expect(res.autopilots[0]?.last_run_status).toBe("some_future_status");
    });
  });

  describe("getConfig", () => {
    it("drops malformed daemon setup URLs instead of throwing", async () => {
      stubFetchJson({
        cdn_domain: "cdn.example.com",
        allow_signup: true,
        daemon_server_url: { wrong: "shape" },
        daemon_app_url: 123,
        workspace_creation_disabled: false,
        feature_flags: { composio_mcp_apps: true },
      });
      const client = new ApiClient("https://api.example.test");
      const config = await client.getConfig();
      expect(config.cdn_domain).toBe("cdn.example.com");
      expect(config.allow_signup).toBe(true);
      expect(config.daemon_server_url).toBeUndefined();
      expect(config.daemon_app_url).toBeUndefined();
      expect(config.feature_flags?.composio_mcp_apps).toBe(true);
    });
  });

  describe("listGroupedIssues", () => {
    it("falls back to empty groups when the response is malformed", async () => {
      stubFetchJson({ groups: "not-an-array" });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listGroupedIssues({ group_by: "assignee" });
      expect(res).toEqual({ groups: [] });
    });
  });

  describe("listComments", () => {
    it("returns [] when the response is not an array", async () => {
      stubFetchJson({ wrong: "shape" });
      const client = new ApiClient("https://api.example.test");
      const comments = await client.listComments("issue-1");
      expect(comments).toEqual([]);
    });
  });

  describe("previewCommentTriggers", () => {
    it("returns an empty agent list when the response is malformed", async () => {
      stubFetchJson({ agents: "not-an-array" });
      const client = new ApiClient("https://api.example.test");
      const preview = await client.previewCommentTriggers("issue-1", "hello");
      expect(preview).toEqual({ agents: [] });
    });
  });

  describe("listIssueSubscribers", () => {
    it("returns [] when the response is null", async () => {
      stubFetchJson(null);
      const client = new ApiClient("https://api.example.test");
      const subs = await client.listIssueSubscribers("issue-1");
      expect(subs).toEqual([]);
    });
  });

  describe("listChildIssues", () => {
    it("returns { issues: [] } when the issues field is missing", async () => {
      stubFetchJson({});
      const client = new ApiClient("https://api.example.test");
      const res = await client.listChildIssues("issue-1");
      expect(res).toEqual({ issues: [] });
    });
  });

  // Agent template catalog is hit by the desktop create-agent picker.
  // Installed desktop builds outlive any given server, so the shape MUST
  // survive future field renames / wrapping without crashing. Each test
  // here mirrors a concrete future drift we want to absorb.
  describe("listAgentTemplates", () => {
    it("falls back to [] when the body is null", async () => {
      stubFetchJson(null);
      const client = new ApiClient("https://api.example.test");
      const tmpls = await client.listAgentTemplates();
      expect(tmpls).toEqual([]);
    });

    it("defaults skills to [] when the field is missing from a template", async () => {
      // Future server: drops `skills` because the picker no longer reads
      // them. Picker code calls `template.skills.length` — must not throw.
      stubFetchJson([{ slug: "x", name: "X" }]);
      const client = new ApiClient("https://api.example.test");
      const tmpls = await client.listAgentTemplates();
      expect(tmpls).toHaveLength(1);
      expect(tmpls[0]?.skills).toEqual([]);
    });

    it("accepts the bare-array shape (current contract)", async () => {
      stubFetchJson([
        { slug: "a", name: "A", description: "", skills: [] },
        { slug: "b", name: "B", description: "", skills: [] },
      ]);
      const client = new ApiClient("https://api.example.test");
      const tmpls = await client.listAgentTemplates();
      expect(tmpls.map((t) => t.slug)).toEqual(["a", "b"]);
    });

    it("accepts a future {templates: [...]} envelope without breaking", async () => {
      // Server migrates to a paginated envelope. We unwrap so the picker
      // keeps working on the older bare-array consumer.
      stubFetchJson({
        templates: [{ slug: "a", name: "A", description: "", skills: [] }],
        total: 1,
      });
      const client = new ApiClient("https://api.example.test");
      const tmpls = await client.listAgentTemplates();
      expect(tmpls).toHaveLength(1);
      expect(tmpls[0]?.slug).toBe("a");
    });
  });

  describe("getAgentTemplate", () => {
    it("falls back to a minimal record carrying the requested slug", async () => {
      // Slug is part of the URL the user clicked — the fallback round-
      // trips it so the page header still makes sense after a parse miss.
      stubFetchJson({ wrong: "shape" });
      const client = new ApiClient("https://api.example.test");
      const detail = await client.getAgentTemplate("code-reviewer");
      expect(detail.slug).toBe("code-reviewer");
      expect(detail.skills).toEqual([]);
      expect(detail.instructions).toBe("");
    });

    it("defaults instructions to '' when the field is missing", async () => {
      stubFetchJson({
        slug: "code-reviewer",
        name: "Code Reviewer",
        description: "",
        skills: [],
      });
      const client = new ApiClient("https://api.example.test");
      const detail = await client.getAgentTemplate("code-reviewer");
      expect(detail.instructions).toBe("");
    });
  });

  describe("listAutopilotDeliveries", () => {
    it("falls back to an empty list when the body is null", async () => {
      stubFetchJson(null);
      const client = new ApiClient("https://api.example.test");
      const res = await client.listAutopilotDeliveries("ap-1");
      expect(res).toEqual({ deliveries: [], total: 0 });
    });

    it("falls back to an empty list when `deliveries` is not an array", async () => {
      stubFetchJson({ deliveries: "not-an-array", total: 0 });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listAutopilotDeliveries("ap-1");
      expect(res).toEqual({ deliveries: [], total: 0 });
    });

    it("accepts an unknown future status value rather than dropping the row", async () => {
      // Server-side enum drift (e.g. new `quarantined` state). The list
      // must still surface the row; downstream UI code's `default` arm
      // handles unknown values with a generic visual.
      stubFetchJson({
        deliveries: [
          {
            id: "d-1",
            workspace_id: "ws-1",
            autopilot_id: "ap-1",
            trigger_id: "t-1",
            provider: "github",
            event: "pull_request.opened",
            dedupe_key: "abc",
            dedupe_source: "x-github-delivery",
            signature_status: "valid",
            status: "quarantined",
            attempt_count: 1,
            content_type: "application/json",
            response_status: 200,
            autopilot_run_id: null,
            replayed_from_delivery_id: null,
            error: null,
            received_at: "2026-01-01T00:00:00Z",
            last_attempt_at: "2026-01-01T00:00:00Z",
            created_at: "2026-01-01T00:00:00Z",
          },
        ],
        total: 1,
      });
      const client = new ApiClient("https://api.example.test");
      const res = await client.listAutopilotDeliveries("ap-1");
      expect(res.deliveries).toHaveLength(1);
      expect(res.deliveries[0]?.status).toBe("quarantined");
      expect(res.deliveries[0]?.dispatch_attempts).toBe(0);
      expect(res.deliveries[0]?.available_at).toBe("");
    });
  });

  describe("getAutopilotDelivery", () => {
    it("falls back to a placeholder carrying the requested id", async () => {
      stubFetchJson({ wrong: "shape" });
      const client = new ApiClient("https://api.example.test");
      const detail = await client.getAutopilotDelivery("ap-1", "d-1");
      expect(detail.id).toBe("d-1");
      expect(detail.autopilot_id).toBe("ap-1");
    });
  });

  describe("committed agent/profile identities", () => {
    it("does not report an empty agent id as a successful create", async () => {
      stubFetchJson({
        id: "",
        workspace_id: "workspace-1",
        runtime_id: "rt-1",
      });
      const client = new ApiClient("https://api.example.test");
      await expect(
        client.createAgent({
          name: "Agent",
          runtime_id: "rt-1",
        }),
      ).rejects.toBeInstanceOf(CommittedResponseUnreadableError);
    });

    it("does not report an empty profile id as a successful create", async () => {
      stubFetchJson({
        id: "",
        workspace_id: "workspace-1",
      });
      const client = new ApiClient("https://api.example.test");
      await expect(
        client.createRuntimeProfile("workspace-1", {
          display_name: "Local Codex",
          protocol_family: "codex",
          command_name: "codex",
        }),
      ).rejects.toBeInstanceOf(CommittedResponseUnreadableError);
    });
  });

  describe("createAgentFromTemplate", () => {
    it("reports an uncertain committed result when the response is malformed", async () => {
      stubFetchJson({ unexpected: "shape" });
      const client = new ApiClient("https://api.example.test");
      await expect(
        client.createAgentFromTemplate({
          template_slug: "x",
          name: "X",
          runtime_id: "rt-1",
        }),
      ).rejects.toMatchObject({
        name: "CommittedResponseUnreadableError",
        committed: true,
        endpoint: "POST /api/agents/from-template",
      });
    });

    it("defaults imported_skill_ids / reused_skill_ids to [] when missing", async () => {
      stubFetchJson({
        agent: {
          id: "agent-1",
          workspace_id: "workspace-1",
          runtime_id: "rt-1",
        },
      });
      const client = new ApiClient("https://api.example.test");
      const resp = await client.createAgentFromTemplate({
        template_slug: "x",
        name: "X",
        runtime_id: "rt-1",
      });
      expect(resp.agent.id).toBe("agent-1");
      expect(resp.imported_skill_ids).toEqual([]);
      expect(resp.reused_skill_ids).toEqual([]);
    });
  });

  describe("createAgentBuilderSession", () => {
    it("reports an uncertain committed result when the response is malformed", async () => {
      stubFetchJson({ unexpected: "shape" });
      const client = new ApiClient("https://api.example.test");
      await expect(
        client.createAgentBuilderSession({ runtime_id: "rt-1" }),
      ).rejects.toMatchObject({
        name: "CommittedResponseUnreadableError",
        committed: true,
        endpoint: "POST /api/agent-builder/sessions",
      });
    });

    it("rejects empty committed identities", async () => {
      stubFetchJson({
        session_id: "",
        builder_agent_id: "agent-1",
        runtime_id: "rt-1",
      });
      const client = new ApiClient("https://api.example.test");
      await expect(
        client.createAgentBuilderSession({ runtime_id: "rt-1" }),
      ).rejects.toBeInstanceOf(CommittedResponseUnreadableError);
    });
  });

  describe("cronPreview", () => {
    it("returns the parsed next runs", async () => {
      stubFetchJson({
        next_runs: ["2026-07-14T01:00:00Z", "2026-07-14T03:00:00Z"],
      });
      const client = new ApiClient("https://api.example.test");
      const res = await client.cronPreview({
        expr: "0 9-21/2 * * *",
        tz: "Asia/Shanghai",
      });
      expect(res).toEqual({
        next_runs: ["2026-07-14T01:00:00Z", "2026-07-14T03:00:00Z"],
      });
    });

    it("URL-encodes the expression and timezone", async () => {
      stubFetchJson({ next_runs: [] });
      const client = new ApiClient("https://api.example.test");
      await client.cronPreview({ expr: "0 9-21/2 * * 2-4", tz: "Asia/Shanghai" });
      const url = String(vi.mocked(fetch).mock.calls[0]?.[0]);
      expect(url).toContain("/api/autopilots/cron-preview?");
      expect(url).toContain("expr=0+9-21%2F2+*+*+2-4");
      expect(url).toContain("tz=Asia%2FShanghai");
    });

    it("returns an empty list verbatim when the expression never fires", async () => {
      stubFetchJson({ next_runs: [] });
      const client = new ApiClient("https://api.example.test");
      const res = await client.cronPreview({ expr: "0 0 30 2 *", tz: "UTC" });
      expect(res).toEqual({ next_runs: [] });
    });

    it("falls back to null when the response is malformed", async () => {
      // null, not [] — the caller must be able to tell "unreadable response"
      // apart from "this expression never fires".
      stubFetchJson({ next_runs: "not-an-array" });
      const client = new ApiClient("https://api.example.test");
      const res = await client.cronPreview({ expr: "0 9 * * *", tz: "UTC" });
      expect(res).toEqual({ next_runs: null });
    });

    it("falls back to null when the field is missing entirely", async () => {
      stubFetchJson({ runs: ["2026-07-14T01:00:00Z"] });
      const client = new ApiClient("https://api.example.test");
      const res = await client.cronPreview({ expr: "0 9 * * *", tz: "UTC" });
      expect(res).toEqual({ next_runs: null });
    });
  });
});

// Direct tests for the helper, decoupled from any specific endpoint —
// guards against an endpoint refactor masking a regression in the helper.
describe("parseWithFallback", () => {
  const opts = { endpoint: "TEST /unit" };

  it("returns parsed data on success", () => {
    const schema = z.object({ id: z.string() });
    const out = parseWithFallback({ id: "x" }, schema, { id: "fallback" }, opts);
    expect(out).toEqual({ id: "x" });
  });

  it("returns the fallback when validation fails", () => {
    const schema = z.object({ id: z.string() });
    const fallback = { id: "fallback" };
    const out = parseWithFallback({ id: 123 }, schema, fallback, opts);
    expect(out).toBe(fallback);
  });

  it("returns the fallback when data is null", () => {
    const schema = z.object({ id: z.string() });
    const fallback = { id: "fallback" };
    const out = parseWithFallback(null, schema, fallback, opts);
    expect(out).toBe(fallback);
  });
});
