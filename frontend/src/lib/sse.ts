/** Minimal EventSource wrapper that tracks the last event id for reconnect. */

export type SSEHandlers = {
  onEvent?: (name: string, data: string, id: string | null) => void;
  onError?: (err: Event) => void;
  onOpen?: () => void;
};

export function openSSE(url: string, handlers: SSEHandlers): () => void {
  const source = new EventSource(url);

  const forward = (name: string) => (ev: MessageEvent<string>) => {
    handlers.onEvent?.(name, ev.data, ev.lastEventId || null);
  };

  source.addEventListener("open", () => handlers.onOpen?.());
  source.addEventListener("error", (ev) => handlers.onError?.(ev));
  source.addEventListener("event", forward("event") as EventListener);
  source.addEventListener("chunk", forward("chunk") as EventListener);
  source.addEventListener("done", forward("done") as EventListener);
  source.onmessage = (ev) => handlers.onEvent?.("message", ev.data, ev.lastEventId || null);

  return () => source.close();
}

export function streamURL(
  kind: "events" | "logs",
  jobId: string,
  after?: number,
): string {
  const q = after != null && after > 0 ? `?after=${after}` : "";
  return `/api/v1/jobs/${encodeURIComponent(jobId)}/${kind}/stream${q}`;
}
