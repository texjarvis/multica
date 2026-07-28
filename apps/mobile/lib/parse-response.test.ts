import { afterEach, describe, expect, it, vi } from "vitest";
import { z } from "zod";
import { parseWithFallback } from "./parse-response";

describe("parseWithFallback logging", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("logs only value-free issue metadata", () => {
    const secret = "sentinel-mobile-parse-secret";
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    expect(
      parseWithFallback(
        { records: { [secret]: "" } },
        z.object({
          records: z.record(z.string(), z.string().min(1)),
        }),
        null,
        { endpoint: "agent" },
      ),
    ).toBeNull();

    expect(warn).toHaveBeenCalledOnce();
    const encoded = JSON.stringify(warn.mock.calls);
    expect(encoded).not.toContain(secret);
    expect(encoded).not.toContain('"received"');
    expect(encoded).toContain("<field>");
    expect(encoded).toContain("issue_count");
    expect(encoded).toContain("too_small");
  });
});
