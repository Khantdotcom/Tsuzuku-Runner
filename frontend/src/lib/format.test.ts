import { describe, expect, it } from "vitest";

import { durationBetween, formatBytes, reservedPercent, shortRepo } from "./format";

describe("shortRepo", () => {
  it("strips host and .git", () => {
    expect(shortRepo("https://github.com/octocat/Hello-World.git")).toBe(
      "octocat/Hello-World",
    );
  });
});

describe("durationBetween", () => {
  it("formats short durations", () => {
    const start = "2026-01-01T00:00:00Z";
    const end = "2026-01-01T00:01:05Z";
    expect(durationBetween(start, end, Date.parse(end))).toBe("1m 5s");
  });
});

describe("formatBytes", () => {
  it("picks a readable unit", () => {
    expect(formatBytes(500)).toBe("500 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
  });
});

describe("reservedPercent", () => {
  it("clamps reserved capacity to 0–100", () => {
    expect(reservedPercent(500, 1000)).toBe(50);
    expect(reservedPercent(0, 1000)).toBe(0);
    expect(reservedPercent(2000, 1000)).toBe(100);
    expect(reservedPercent(10, 0)).toBe(0);
  });
});
