import { SlidingNumber } from "@/components/animate-ui/primitives/texts/sliding-number";
import { GlowCard } from "@/components/glow-card";
import type { Worker } from "@/lib/api";
import { formatAgo, formatCores, formatMemory } from "@/lib/format";
import { cn } from "@/lib/utils";

function clampPercent(value: number): number {
  return Math.min(100, Math.max(0, value));
}

function UsageBar({ percent, active }: { percent: number; active: boolean }) {
  return (
    <div className="h-1.5 overflow-hidden rounded-full bg-muted">
      <div
        className={cn(
          "h-full rounded-full transition-[width] duration-700 ease-out",
          active
            ? "bg-gradient-to-r from-indigo-500 via-violet-500 to-fuchsia-500 shadow-[0_0_12px_var(--glow)]"
            : "bg-muted-foreground/40",
        )}
        style={{ width: `${clampPercent(percent)}%` }}
      />
    </div>
  );
}

function StatusPill({ online }: { online: boolean }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-xs font-medium",
        online
          ? "border-success/30 bg-success/10 text-success"
          : "border-border bg-muted text-muted-foreground",
      )}
    >
      <span className="relative flex size-2">
        {online && (
          <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-60" />
        )}
        <span
          className={cn(
            "relative inline-flex size-2 rounded-full",
            online ? "bg-success" : "bg-muted-foreground/60",
          )}
        />
      </span>
      {online ? "Online" : "Offline"}
    </span>
  );
}

function formatVersion(version: string): string {
  return /^\d/.test(version) ? `v${version}` : version;
}

export function WorkerCard({ worker, now }: { worker: Worker; now: number | null }) {
  const online = worker.status === "online";
  const cpu = worker.cpu_used_percent;
  const memUsed = worker.memory_used_mb;
  const memPercent =
    memUsed !== null && worker.memory_mb > 0 ? (memUsed / worker.memory_mb) * 100 : 0;
  const { os, arch, version } = worker.metadata;

  return (
    <GlowCard className={cn(!online && "opacity-70")}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h3 className="truncate font-medium">{worker.name}</h3>
          <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground">
            {[os && arch ? `${os}/${arch}` : null, version ? formatVersion(version) : null]
              .filter(Boolean)
              .join(" · ") || "unknown host"}
          </p>
        </div>
        <StatusPill online={online} />
      </div>

      <div className="mt-5 space-y-4 text-sm">
        <div className="space-y-1.5">
          <div className="flex items-baseline justify-between">
            <span className="text-muted-foreground">CPU</span>
            {cpu === null ? (
              <span className="font-mono text-muted-foreground">—</span>
            ) : (
              <span className="inline-flex items-baseline font-mono tabular-nums">
                <SlidingNumber number={Math.round(cpu)} />%
              </span>
            )}
          </div>
          <UsageBar percent={cpu ?? 0} active={online} />
        </div>

        <div className="space-y-1.5">
          <div className="flex items-baseline justify-between">
            <span className="text-muted-foreground">Memory</span>
            <span className="font-mono tabular-nums">
              {memUsed === null ? "—" : formatMemory(memUsed)} / {formatMemory(worker.memory_mb)}
            </span>
          </div>
          <UsageBar percent={memPercent} active={online} />
        </div>
      </div>

      <div className="mt-5 flex items-center justify-between border-t border-border/60 pt-3 text-xs text-muted-foreground">
        <span>
          {worker.slots} {worker.slots === 1 ? "slot" : "slots"} · {formatCores(worker.cpu_millis)}
        </span>
        <span title={new Date(worker.last_heartbeat_at).toLocaleString()}>
          heartbeat {now === null ? "…" : formatAgo(worker.last_heartbeat_at, now)}
        </span>
      </div>
    </GlowCard>
  );
}
