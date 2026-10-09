"use client";

import { useQuery } from "@tanstack/react-query";
import { Database, Server, Cpu, TriangleAlert } from "lucide-react";
import { useEffect, useState, type ReactNode } from "react";

import { SlidingNumber } from "@/components/animate-ui/primitives/texts/sliding-number";
import { GlowCard } from "@/components/glow-card";
import { WorkerCard } from "@/components/worker-card";
import { listWorkers, probe, type ProbeResult } from "@/lib/api";
import { cn } from "@/lib/utils";

const pollIntervalMs = 5_000;

type Tone = "ok" | "down" | "pending";

function useNow(intervalMs: number): number | null {
  const [now, setNow] = useState<number | null>(null);
  useEffect(() => {
    const tick = () => setNow(Date.now());
    const first = setTimeout(tick, 0);
    const id = setInterval(tick, intervalMs);
    return () => {
      clearTimeout(first);
      clearInterval(id);
    };
  }, [intervalMs]);
  return now;
}

function probeTone(result: ProbeResult | undefined, failed: boolean): Tone {
  if (failed) return "down";
  if (!result) return "pending";
  return result.ok ? "ok" : "down";
}

function StatusTile({
  icon,
  label,
  tone,
  value,
  hint,
}: {
  icon: ReactNode;
  label: string;
  tone: Tone;
  value: ReactNode;
  hint: string;
}) {
  return (
    <GlowCard>
      <div className="flex items-center justify-between">
        <span className="flex items-center gap-2 text-sm text-muted-foreground">
          {icon}
          {label}
        </span>
        <span
          className={cn(
            "size-2.5 rounded-full",
            tone === "ok" && "bg-success shadow-[0_0_10px_var(--success)]",
            tone === "down" && "bg-destructive shadow-[0_0_10px_var(--destructive)]",
            tone === "pending" && "animate-pulse bg-muted-foreground/40",
          )}
        />
      </div>
      <div className="mt-3 text-2xl font-semibold tracking-tight">{value}</div>
      <p className="mt-1 text-xs text-muted-foreground">{hint}</p>
    </GlowCard>
  );
}

export function SystemStatus() {
  const health = useQuery({
    queryKey: ["probe", "healthz"],
    queryFn: () => probe("healthz"),
    refetchInterval: pollIntervalMs,
  });
  const ready = useQuery({
    queryKey: ["probe", "readyz"],
    queryFn: () => probe("readyz"),
    refetchInterval: pollIntervalMs,
  });
  const workers = useQuery({
    queryKey: ["workers"],
    queryFn: listWorkers,
    refetchInterval: pollIntervalMs,
  });
  const now = useNow(1_000);

  const apiTone = probeTone(health.data, health.isError);
  const dbTone = probeTone(ready.data, ready.isError);
  const list = [...(workers.data ?? [])].sort((a, b) => a.name.localeCompare(b.name));
  const online = list.filter((w) => w.status === "online").length;
  const workersTone: Tone = workers.isPending
    ? "pending"
    : workers.isError || online === 0
      ? "down"
      : "ok";

  return (
    <div className="space-y-10">
      <div className="grid gap-4 sm:grid-cols-3">
        <StatusTile
          icon={<Server className="size-4" />}
          label="API server"
          tone={apiTone}
          value={apiTone === "ok" ? "Online" : apiTone === "down" ? "Unreachable" : "Checking…"}
          hint="GET /healthz"
        />
        <StatusTile
          icon={<Database className="size-4" />}
          label="Database"
          tone={dbTone}
          value={dbTone === "ok" ? "Ready" : dbTone === "down" ? "Unavailable" : "Checking…"}
          hint="GET /readyz"
        />
        <StatusTile
          icon={<Cpu className="size-4" />}
          label="Workers online"
          tone={workersTone}
          value={
            workers.isPending ? (
              "Checking…"
            ) : (
              <span className="inline-flex items-baseline gap-1 tabular-nums">
                <SlidingNumber number={online} />
                <span className="text-base font-normal text-muted-foreground">
                  / {list.length}
                </span>
              </span>
            )
          }
          hint="Heartbeat within the stale window"
        />
      </div>

      <section>
        <div className="mb-4 flex items-baseline justify-between">
          <h2 className="text-lg font-semibold tracking-tight">Workers</h2>
          <span className="text-xs text-muted-foreground">
            Refreshes every {pollIntervalMs / 1000}s
          </span>
        </div>

        {workers.isError ? (
          <div className="flex items-center gap-3 rounded-xl border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive">
            <TriangleAlert className="size-4 shrink-0" />
            Could not load workers: {workers.error.message}
          </div>
        ) : workers.isPending ? (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {[0, 1].map((i) => (
              <div key={i} className="h-48 animate-pulse rounded-2xl border bg-muted/50" />
            ))}
          </div>
        ) : list.length === 0 ? (
          <div className="rounded-2xl border border-dashed px-6 py-12 text-center text-sm text-muted-foreground">
            No workers have registered yet. Start one with{" "}
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-foreground">
              docker compose up
            </code>{" "}
            or{" "}
            <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-foreground">
              task run:worker
            </code>
            .
          </div>
        ) : (
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
            {list.map((w) => (
              <WorkerCard key={w.id} worker={w} now={now} />
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
