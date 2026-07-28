import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WSClient } from "./ws-client";

class FakeWebSocket {
  static lastInstance: FakeWebSocket | null = null;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  readyState = 0;

  constructor(_url: string) {
    FakeWebSocket.lastInstance = this;
  }

  close() {}
  send() {}
}

describe("mobile WSClient log redaction", () => {
  beforeEach(() => {
    FakeWebSocket.lastInstance = null;
    vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("never logs malformed or missing-type frame content", () => {
    const logger = {
      info: vi.fn(),
      warn: vi.fn(),
      debug: vi.fn(),
    };
    const client = new WSClient({
      url: "ws://example.test/ws",
      token: "token",
      workspaceSlug: "workspace",
      logger,
    });
    client.connect();

    const parseSentinel = "mobile-ws-parse-secret-sentinel";
    const malformed = `{"custom_env":{"TOKEN":"${parseSentinel}"}`;
    FakeWebSocket.lastInstance!.onmessage?.({ data: malformed });

    const typeSentinel = "mobile-ws-type-secret-sentinel";
    const missingType = JSON.stringify({
      payload: { runtime_config: { token: typeSentinel } },
    });
    FakeWebSocket.lastInstance!.onmessage?.({ data: missingType });

    const numericTypeSentinel = "mobile-ws-numeric-type-secret-sentinel";
    const numericType = JSON.stringify({
      type: 42,
      payload: { mcp_config: { token: numericTypeSentinel } },
    });
    FakeWebSocket.lastInstance!.onmessage?.({ data: numericType });

    expect(logger.warn).toHaveBeenNthCalledWith(
      1,
      "[ws] non-JSON frame ignored",
      { frame_char_count: malformed.length },
    );
    expect(logger.warn).toHaveBeenNthCalledWith(
      2,
      "[ws] frame without type",
      { frame_char_count: missingType.length },
    );
    expect(logger.warn).toHaveBeenNthCalledWith(
      3,
      "[ws] frame without type",
      { frame_char_count: numericType.length },
    );
    const logged = JSON.stringify(logger.warn.mock.calls);
    expect(logged).not.toContain(parseSentinel);
    expect(logged).not.toContain(typeSentinel);
    expect(logged).not.toContain(numericTypeSentinel);
  });
});
