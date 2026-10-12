"use client";

import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { WorkerCard } from "@/components/worker-card";
import { listWorkers, probe } from "@/lib/api";

const pollMs = 5_000;

export function WorkersPage() {
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
  const workers = useQuery({
    queryKey: ["workers"],
    queryFn: listWorkers,
    refetchInterval: pollMs,
  });

  const online = (workers.data ?? []).filter((w) => w.status === "online").length;

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Workers</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          {workers.isPending
            ? "Loading fleet…"
            : `${online} online · API ${ready.data?.ok ? "ready" : "down"}`}
        </p>
      </div>
      {workers.isError && (
        <p className="text-sm text-destructive">{(workers.error as Error).message}</p>
      )}
      <div className="grid gap-4 sm:grid-cols-2">
        {now != null &&
          (workers.data ?? []).map((w) => (
            <WorkerCard key={w.id} worker={w} now={now} />
          ))}
      </div>
      {!workers.isPending && (workers.data?.length ?? 0) === 0 && (
        <p className="text-sm text-muted-foreground">No workers registered yet.</p>
      )}
    </div>
  );
}
