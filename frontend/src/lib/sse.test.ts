import { describe, expect, it } from "vitest";

import { streamURL } from "./sse";

describe("streamURL", () => {
  it("builds event and log stream paths", () => {
    expect(streamURL("events", "abc")).toBe("/api/v1/jobs/abc/events/stream");
    expect(streamURL("logs", "abc", 9)).toBe("/api/v1/jobs/abc/logs/stream?after=9");
  });
});
