"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useEffect, useState } from "react";

import { listQueue, type QueueJob } from "@/lib/api";
import { formatAgo, formatCores, formatMemory, shortRepo } from "@/lib/format";

const pollMs = 5_000;

export function QueuePage() {
  const [now, setNow] = useState<number | null>(null);
  useEffect(() => {
    const tick = () => setNow(Date.now());
    const first = setTimeout(tick, 0);
    const id = setInterval(tick, 1000);
    return () => {
      clearTimeout(first);
      clearInterval(id);
    };
  }, []);

  const queue = useQuery({
    queryKey: ["queue"],
    queryFn: () => listQueue(50),
    refetchInterval: pollMs,
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Queue</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Placeable jobs ordered by priority, then wait time. Jobs that do not
          fit any worker stay here until capacity frees up.
        </p>
      </div>

      {queue.isError && (
        <p className="text-sm text-destructive">{(queue.error as Error).message}</p>
      )}

      <div className="overflow-x-auto rounded-xl border border-border/80">
        <table className="w-full min-w-[40rem] text-left text-sm">
          <thead className="border-b border-border/80 bg-muted/40 text-xs text-muted-foreground">
            <tr>
              <th className="px-3 py-2 font-medium">Job</th>
              <th className="px-3 py-2 font-medium">Priority</th>
              <th className="px-3 py-2 font-medium">CPU</th>
              <th className="px-3 py-2 font-medium">Memory</th>
              <th className="px-3 py-2 font-medium">Runtime</th>
              <th className="px-3 py-2 font-medium">Waiting</th>
            </tr>
          </thead>
          <tbody>
            {(queue.data ?? []).map((job) => (
              <QueueRow key={job.id} job={job} now={now} />
            ))}
          </tbody>
        </table>
        {!queue.isPending && (queue.data?.length ?? 0) === 0 && (
          <p className="px-3 py-6 text-sm text-muted-foreground">Queue is empty.</p>
        )}
      </div>
    </div>
  );
}

function QueueRow({ job, now }: { job: QueueJob; now: number | null }) {
  return (
    <tr className="border-b border-border/50 last:border-0">
      <td className="px-3 py-2.5">
        <Link href={`/jobs/${job.number}`} className="font-medium hover:underline">
          #{job.number}
        </Link>
        <div className="truncate text-xs text-muted-foreground" title={job.repository}>
          {shortRepo(job.repository)} @ {job.revision}
        </div>
      </td>
      <td className="px-3 py-2.5 font-mono tabular-nums">{job.priority}</td>
      <td className="px-3 py-2.5 font-mono tabular-nums">{formatCores(job.cpu_millis)}</td>
      <td className="px-3 py-2.5 font-mono tabular-nums">{formatMemory(job.memory_mb)}</td>
      <td className="px-3 py-2.5 font-mono text-xs">{job.image}</td>
      <td className="px-3 py-2.5 text-muted-foreground">
        {now === null ? "…" : formatAgo(job.created_at, now)}
      </td>
    </tr>
  );
}
