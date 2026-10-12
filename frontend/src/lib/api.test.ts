import { describe, expect, it } from "vitest";

import {
  decodeLogData,
  isCancellableState,
  isTerminalState,
} from "./api";

describe("decodeLogData", () => {
  it("decodes base64 UTF-8", () => {
    const encoded = Buffer.from("hello\n世界", "utf8").toString("base64");
    expect(decodeLogData(encoded)).toBe("hello\n世界");
  });
});

describe("job state helpers", () => {
  it("recognises terminal and cancellable states", () => {
    expect(isTerminalState("COMPLETED")).toBe(true);
    expect(isTerminalState("EXECUTING")).toBe(false);
    expect(isCancellableState("QUEUED")).toBe(true);
    expect(isCancellableState("FAILED")).toBe(false);
  });
});
