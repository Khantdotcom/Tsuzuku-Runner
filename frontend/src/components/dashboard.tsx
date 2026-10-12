"use client";

import { useQuery } from "@tanstack/react-query";
import Link from "next/link";
import { useEffect, useState } from "react";

import { JobStateBadge } from "@/components/job-state-badge";
import { getStats, listJobs, probe, type JobState, type Stats } from "@/lib/api";
import { durationBetween, formatAgo, shortRepo } from "@/lib/format";
import { cn } from "@/lib/utils";

const pollMs = 5_000;

const statCards: Array<{ key: keyof Stats; label: string; hint: string }> = [
  { key: "running", label: "Running", hint: "Preparing, executing, verifying" },
  { key: "queued", label: "Queued", hint: "Waiting to be placed" },
  { key: "scheduled", label: "Scheduled", hint: "Assigned, not yet claimed" },
  { key: "failed", label: "Failed", hint: "Ended with a failure" },
  { key: "completed", label: "Completed", hint: "Passed verification" },
  { key: "cancelled", label: "Cancelled", hint: "Stopped on request" },
];

export function Dashboard() {
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

  const ready = useQuery({
    queryKey: ["probe", "readyz"],
    queryFn: () => probe("readyz"),
    refetchInterval: pollMs,
  });
  const stats = useQuery({
    queryKey: ["stats"],
    queryFn: getStats,
    refetchInterval: pollMs,
  });
  const jobs = useQuery({
    queryKey: ["jobs", "recent"],
    queryFn: () => listJobs({ limit: 20 }),
    refetchInterval: pollMs,
  });

  return (
    <div className="space-y-8">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Dashboard</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            API{" "}
            <span className={ready.data?.ok ? "text-success" : "text-destructive"}>
              {ready.isPending ? "checking…" : ready.data?.ok ? "ready" : "unreachable"}
            </span>
          </p>
        </div>
        <Link
          href="/jobs/new"
          className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground"
        >
          New job
        </Link>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {statCards.map((card) => (
          <div
            key={card.key}
            className="rounded-xl border border-border/80 bg-card px-4 py-3 shadow-sm"
          >
            <div className="text-sm text-muted-foreground">{card.label}</div>
            <div className="mt-1 text-3xl font-semibold tabular-nums tracking-tight">
              {stats.data?.[card.key] ?? "—"}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">{card.hint}</p>
          </div>
        ))}
      </div>

      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-medium">Recent jobs</h2>
          {jobs.isError && (
            <p className="text-sm text-destructive">{(jobs.error as Error).message}</p>
          )}
        </div>
        <div className="overflow-hidden rounded-xl border border-border/80">
          <table className="w-full text-left text-sm">
            <thead className="border-b border-border/80 bg-muted/40 text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">Job</th>
                <th className="px-4 py-2 font-medium">State</th>
                <th className="hidden px-4 py-2 font-medium md:table-cell">Workload</th>
                <th className="px-4 py-2 font-medium">Duration</th>
                <th className="hidden px-4 py-2 font-medium sm:table-cell">Updated</th>
              </tr>
            </thead>
            <tbody>
              {(jobs.data?.jobs ?? []).map((job) => (
                <tr key={job.id} className="border-b border-border/60 last:border-0">
                  <td className="px-4 py-3">
                    <Link
                      href={`/jobs/${job.number}`}
                      className={cn("font-medium text-primary hover:underline")}
                    >
                      #{job.number}
                    </Link>
                  </td>
                  <td className="px-4 py-3">
                    <JobStateBadge state={job.state as JobState} />
                  </td>
                  <td className="hidden max-w-xs truncate px-4 py-3 text-muted-foreground md:table-cell">
                    {shortRepo(job.workload.repository)}@{job.workload.revision}
                  </td>
                  <td className="px-4 py-3 tabular-nums text-muted-foreground">
                    {now
                      ? durationBetween(job.started_at, job.finished_at, now)
                      : "—"}
                  </td>
                  <td className="hidden px-4 py-3 text-muted-foreground sm:table-cell">
                    {now ? formatAgo(job.updated_at, now) : "—"}
                  </td>
                </tr>
              ))}
              {!jobs.isPending && (jobs.data?.jobs.length ?? 0) === 0 && (
                <tr>
                  <td colSpan={5} className="px-4 py-8 text-center text-muted-foreground">
                    No jobs yet.{" "}
                    <Link href="/jobs/new" className="text-primary hover:underline">
                      Submit one
                    </Link>
                    .
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
