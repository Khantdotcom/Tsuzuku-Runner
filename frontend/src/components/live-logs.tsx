"use client";

import { useEffect, useRef, useState } from "react";

import { decodeLogData, type LogChunk } from "@/lib/api";
import { openSSE, streamURL } from "@/lib/sse";
import { cn } from "@/lib/utils";

type Line = { id: number; stream: string; text: string };

export function LiveLogs({
  jobId,
}: {
  jobId: string;
  /** Kept for callers; the stream always drains until done. */
  active?: boolean;
}) {
  const [lines, setLines] = useState<Line[]>([]);
  const [paused, setPaused] = useState(false);
  const [status, setStatus] = useState<"connecting" | "live" | "done" | "error">(
    "connecting",
  );
  const bottomRef = useRef<HTMLDivElement>(null);
  const afterRef = useRef(0);

  useEffect(() => {
    const close = openSSE(streamURL("logs", jobId, afterRef.current || undefined), {
      onOpen: () => setStatus("live"),
      onError: () => setStatus((s) => (s === "done" ? s : "error")),
      onEvent: (name, data, id) => {
        if (name === "done") {
          setStatus("done");
          return;
        }
        if (name !== "chunk") return;
        try {
          const chunk = JSON.parse(data) as LogChunk;
          const text = decodeLogData(chunk.data);
          afterRef.current = chunk.id;
          if (id) {
            const n = Number(id);
            if (!Number.isNaN(n)) afterRef.current = n;
          }
          setLines((prev) => [...prev, { id: chunk.id, stream: chunk.stream, text }]);
        } catch {
          setStatus("error");
        }
      },
    });
    return close;
  }, [jobId]);

  useEffect(() => {
    if (!paused) {
      bottomRef.current?.scrollIntoView({ behavior: "smooth" });
    }
  }, [lines, paused]);

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          {status === "connecting" && "Connecting…"}
          {status === "live" && "Live"}
          {status === "done" && "Stream finished"}
          {status === "error" && "Stream interrupted"}
        </p>
        <button
          type="button"
          onClick={() => setPaused((p) => !p)}
          className="rounded-md border border-border px-2 py-1 text-xs"
        >
          {paused ? "Resume auto-scroll" : "Pause auto-scroll"}
        </button>
      </div>
      <pre className="max-h-[28rem] overflow-auto rounded-xl border border-border/80 bg-zinc-950 p-4 font-mono text-xs leading-relaxed text-zinc-100">
        {lines.length === 0 && (
          <span className="text-zinc-500">Waiting for output…</span>
        )}
        {lines.map((line) => (
          <span
            key={line.id}
            className={cn(line.stream === "stderr" && "text-red-300")}
          >
            {line.text}
          </span>
        ))}
        <div ref={bottomRef} />
      </pre>
    </div>
  );
}
