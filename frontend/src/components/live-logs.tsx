"use client";

import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";

import {
  decodeLogData,
  getJob,
  isTerminalState,
  listLogs,
  type LogChunk,
} from "@/lib/api";
import { cn } from "@/lib/utils";

type Line = { id: number; stream: string; text: string };
type Status = "connecting" | "live" | "done" | "error";

const pollMs = 1_000;

function mergeChunks(
  prev: Line[],
  seen: Set<number>,
  chunks: LogChunk[],
): Line[] {
  if (chunks.length === 0) return prev;
  const next = [...prev];
  for (const chunk of chunks) {
    if (seen.has(chunk.id)) continue;
    seen.add(chunk.id);
    next.push({
      id: chunk.id,
      stream: chunk.stream,
      text: decodeLogData(chunk.data),
    });
  }
  return next;
}

export function LiveLogs({
  jobId,
}: {
  jobId: string;
  /** Kept for callers; polling drains until the job is terminal and quiet. */
  active?: boolean;
}) {
  const [lines, setLines] = useState<Line[]>([]);
  const [paused, setPaused] = useState(false);
  const [status, setStatus] = useState<Status>("connecting");
  const bottomRef = useRef<HTMLDivElement>(null);
  const afterRef = useRef(0);
  const seenRef = useRef(new Set<number>());

  const job = useQuery({
    queryKey: ["job", jobId],
    queryFn: () => getJob(jobId),
    refetchInterval: (q) =>
      q.state.data && isTerminalState(q.state.data.state) ? false : pollMs,
  });

  const terminal = !!job.data && isTerminalState(job.data.state);

  useEffect(() => {
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let emptyWhileTerminal = 0;

    const schedule = () => {
      timer = setTimeout(() => {
        void tick();
      }, pollMs);
    };

    const tick = async () => {
      try {
        const page = await listLogs(jobId, afterRef.current, 200);
        if (cancelled) return;
        setStatus("live");
        if (page.chunks.length > 0) {
          emptyWhileTerminal = 0;
          afterRef.current = page.next_after;
          setLines((prev) => mergeChunks(prev, seenRef.current, page.chunks));
        } else if (terminal) {
          emptyWhileTerminal += 1;
          if (emptyWhileTerminal >= 2) {
            setStatus("done");
            return;
          }
        }
      } catch {
        if (!cancelled) setStatus((s) => (s === "done" ? s : "error"));
      }
      if (!cancelled) schedule();
    };

    void tick();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [jobId, terminal]);

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
          {status === "live" && "Live (polling)"}
          {status === "done" && "Finished"}
          {status === "error" && "Poll interrupted"}
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
