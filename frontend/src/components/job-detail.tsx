"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useEffect, useState } from "react";

import { JobStateBadge } from "@/components/job-state-badge";
import { LiveLogs } from "@/components/live-logs";
import {
  APIError,
  cancelJob,
  getEvidence,
  getJob,
  isCancellableState,
  isTerminalState,
  listAttempts,
  listDeliveries,
  retryDeliveries,
} from "@/lib/api";
import { durationBetween, formatAgo, formatBytes, shortRepo } from "@/lib/format";
import { cn } from "@/lib/utils";

type Tab =
  | "overview"
  | "timeline"
  | "logs"
  | "verification"
  | "artifacts"
  | "deliveries"
  | "failures";

const tabs: Tab[] = [
  "overview",
  "timeline",
  "logs",
  "verification",
  "artifacts",
  "deliveries",
  "failures",
];

export function JobDetailView({ id }: { id: string }) {
  const [tab, setTab] = useState<Tab>("overview");
  const [now, setNow] = useState<number | null>(null);
  const qc = useQueryClient();

  useEffect(() => {
    const tick = () => setNow(Date.now());
    const first = setTimeout(tick, 0);
    const iv = setInterval(tick, 1000);
    return () => {
      clearTimeout(first);
      clearInterval(iv);
    };
  }, []);

  const job = useQuery({
    queryKey: ["job", id],
    queryFn: () => getJob(id),
    refetchInterval: (q) =>
      q.state.data && isTerminalState(q.state.data.state) ? false : 2_000,
  });

  const attempts = useQuery({
    queryKey: ["attempts", id],
    queryFn: () => listAttempts(id),
    enabled: !!job.data,
    refetchInterval: () =>
      job.data && isTerminalState(job.data.state) ? false : 2_000,
  });
  const evidence = useQuery({
    queryKey: ["evidence", id],
    queryFn: () => getEvidence(id),
    enabled: !!job.data,
    refetchInterval: () =>
      job.data && isTerminalState(job.data.state) ? false : 2_000,
  });
  const deliveries = useQuery({
    queryKey: ["deliveries", id],
    queryFn: () => listDeliveries(id),
    enabled: !!job.data && isTerminalState(job.data.state),
  });

  const cancel = useMutation({
    mutationFn: () => cancelJob(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["job", id] });
      void qc.invalidateQueries({ queryKey: ["attempts", id] });
      void qc.invalidateQueries({ queryKey: ["evidence", id] });
    },
  });
  const retryDelivery = useMutation({
    mutationFn: () => retryDeliveries(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["deliveries", id] });
    },
  });

  if (job.isPending) {
    return <p className="text-sm text-muted-foreground">Loading job…</p>;
  }
  if (job.isError) {
    return (
      <p className="text-sm text-destructive">
        {(job.error as Error).message}{" "}
        <Link href="/" className="underline">
          Back
        </Link>
      </p>
    );
  }

  const j = job.data;
  const spec = j.workload.spec as {
    repository?: string;
    revision?: string;
    command?: string;
    verification?: { command?: string };
    resources?: { cpu?: number; memory_mb?: number };
    timeout_seconds?: number;
    runtime?: { image?: string; network?: boolean };
  };

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-3">
            <h1 className="text-2xl font-semibold tracking-tight">Job #{j.number}</h1>
            <JobStateBadge state={j.state} />
          </div>
          <p className="text-sm text-muted-foreground">
            {shortRepo(String(spec.repository ?? ""))} @ {String(spec.revision ?? "")}
          </p>
          {now && (
            <p className="text-xs text-muted-foreground">
              Duration {durationBetween(j.started_at, j.finished_at, now)}
              {j.updated_at ? ` · updated ${formatAgo(j.updated_at, now)}` : ""}
            </p>
          )}
        </div>
        {isCancellableState(j.state) && (
          <button
            type="button"
            disabled={cancel.isPending}
            onClick={() => cancel.mutate()}
            className="rounded-md border border-destructive/40 px-3 py-1.5 text-sm text-destructive hover:bg-destructive/10 disabled:opacity-60"
          >
            {cancel.isPending ? "Cancelling…" : "Cancel"}
          </button>
        )}
      </div>
      {cancel.isError && (
        <p className="text-sm text-destructive">
          {cancel.error instanceof APIError ? cancel.error.message : "Cancel failed"}
        </p>
      )}

      <div className="flex flex-wrap gap-1 border-b border-border/80 pb-px">
        {tabs.map((t) => (
          <button
            key={t}
            type="button"
            onClick={() => setTab(t)}
            className={cn(
              "rounded-t-md px-3 py-2 text-sm capitalize",
              tab === t
                ? "border border-b-background border-border/80 bg-background font-medium"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {t}
          </button>
        ))}
      </div>

      {tab === "overview" && (
        <div className="grid gap-4 md:grid-cols-2">
          <Card title="Workload">
            <KV label="Command" value={<code className="text-xs">{String(spec.command ?? "")}</code>} />
            <KV
              label="Verification"
              value={spec.verification?.command ? <code className="text-xs">{spec.verification.command}</code> : "—"}
            />
            <KV label="Image" value={String(spec.runtime?.image ?? "—")} />
            <KV
              label="Resources"
              value={`${spec.resources?.cpu ?? "—"} CPU · ${spec.resources?.memory_mb ?? "—"} MB · ${spec.timeout_seconds ?? "—"}s`}
            />
            <KV label="Scheduled" value={j.scheduled_at ?? "as soon as possible"} />
          </Card>
          <Card title="Attempts">
            {(attempts.data ?? []).length === 0 && (
              <p className="text-sm text-muted-foreground">No attempts yet.</p>
            )}
            {(attempts.data ?? []).map((a) => (
              <div key={a.id} className="border-b border-border/50 py-2 text-sm last:border-0">
                <div className="font-medium">Attempt {a.attempt_number}</div>
                <div className="text-muted-foreground">
                  {a.status}
                  {a.exit_code != null ? ` · exit ${a.exit_code}` : ""}
                  {a.error ? ` · ${a.error}` : ""}
                </div>
              </div>
            ))}
          </Card>
        </div>
      )}

      {tab === "timeline" && (
        <Card title="State history">
          <ol className="space-y-3">
            {j.transitions.map((t, i) => (
              <li key={`${t.at}-${i}`} className="flex gap-3 text-sm">
                <span className="mt-1 size-2 shrink-0 rounded-full bg-primary" />
                <div>
                  <div className="font-medium">
                    {t.from ? `${t.from} → ${t.to}` : t.to}
                  </div>
                  <div className="text-muted-foreground">
                    {t.actor}: {t.reason}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {now ? formatAgo(t.at, now) : t.at}
                  </div>
                </div>
              </li>
            ))}
          </ol>
        </Card>
      )}

      {tab === "logs" && (
        <LiveLogs jobId={j.id} />
      )}

      {tab === "verification" && (
        <div className="space-y-4">
          {(evidence.data?.verifications ?? []).length === 0 && (
            <p className="text-sm text-muted-foreground">No verification runs yet.</p>
          )}
          {(evidence.data?.verifications ?? []).map((v) => {
            const byKind = new Map<string, typeof v.checks>();
            for (const c of v.checks) {
              const list = byKind.get(c.kind) ?? [];
              list.push(c);
              byKind.set(c.kind, list);
            }
            const kindOrder = ["exit_code", "command", "acceptance", "integrity"];
            const kinds = [
              ...kindOrder.filter((k) => byKind.has(k)),
              ...[...byKind.keys()].filter((k) => !kindOrder.includes(k)),
            ];
            return (
              <Card key={v.id} title={`Run ${v.status}`}>
                {kinds.map((kind) => (
                  <div key={kind} className="mb-3 last:mb-0">
                    <div className="mb-1 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                      {kind}
                    </div>
                    {(byKind.get(kind) ?? []).map((c) => (
                      <div key={c.name} className="border-b border-border/50 py-2 text-sm last:border-0">
                        <div className="flex items-center justify-between gap-2">
                          <span className="font-medium">{c.name}</span>
                          <span>{c.status}</span>
                        </div>
                        {c.command && <code className="text-xs text-muted-foreground">{c.command}</code>}
                        {c.output && (
                          <pre className="mt-2 max-h-40 overflow-auto rounded bg-muted p-2 text-xs">
                            {c.output}
                          </pre>
                        )}
                      </div>
                    ))}
                  </div>
                ))}
              </Card>
            );
          })}
        </div>
      )}

      {tab === "artifacts" && (
        <Card title="Artifacts">
          <div className="mb-3">
            <a
              href={`/api/v1/jobs/${j.id}/evidence/bundle`}
              className="text-sm text-primary hover:underline"
            >
              Download evidence zip
            </a>
          </div>
          {(evidence.data?.artifacts ?? []).length === 0 && (
            <p className="text-sm text-muted-foreground">No artifacts yet.</p>
          )}
          <ul className="divide-y divide-border/60">
            {(evidence.data?.artifacts ?? []).map((a) => (
              <li key={a.id} className="flex items-center justify-between gap-3 py-2 text-sm">
                <div>
                  <div className="font-medium">{a.name}</div>
                  <div className="text-xs text-muted-foreground">
                    {formatBytes(a.size_bytes)} · {a.sha256.slice(0, 12)}…
                  </div>
                </div>
                <a href={a.url} className="text-primary hover:underline">
                  Download
                </a>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {tab === "deliveries" && (
        <Card title="Deliveries">
          {j.state === "COMPLETED" && (
            <div className="mb-3">
              <button
                type="button"
                disabled={retryDelivery.isPending}
                onClick={() => retryDelivery.mutate()}
                className="rounded-md border border-border px-3 py-1.5 text-sm hover:bg-muted disabled:opacity-60"
              >
                {retryDelivery.isPending ? "Retrying…" : "Retry deliveries"}
              </button>
              {retryDelivery.isError && (
                <p className="mt-2 text-sm text-destructive">
                  {retryDelivery.error instanceof APIError
                    ? retryDelivery.error.message
                    : "Retry failed"}
                </p>
              )}
            </div>
          )}
          {(deliveries.data ?? []).length === 0 && (
            <p className="text-sm text-muted-foreground">
              No deliveries yet. PATCH appears when a diff.patch artifact exists; GitHub PR
              requires delivery.github_pr and TSUZUKU_GITHUB_TOKEN.
            </p>
          )}
          <ul className="divide-y divide-border/60">
            {(deliveries.data ?? []).map((d) => (
              <li key={d.id} className="space-y-1 py-2 text-sm">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <span className="font-medium">
                    {d.kind} · {d.status}
                  </span>
                  {d.external_ref?.startsWith("http") ? (
                    <a
                      href={d.external_ref}
                      target="_blank"
                      rel="noreferrer"
                      className="text-primary hover:underline"
                    >
                      Open
                    </a>
                  ) : d.external_ref?.startsWith("/api/") ? (
                    <a href={d.external_ref} className="text-primary hover:underline">
                      Download
                    </a>
                  ) : null}
                </div>
                <div className="text-xs text-muted-foreground">
                  {d.target || "—"}
                  {d.external_ref && !d.external_ref.startsWith("http") && !d.external_ref.startsWith("/api/")
                    ? ` · ${d.external_ref}`
                    : ""}
                </div>
              </li>
            ))}
          </ul>
        </Card>
      )}

      {tab === "failures" && (
        <Card title="Failures">
          {(evidence.data?.failures ?? []).length === 0 && (
            <p className="text-sm text-muted-foreground">No failures recorded.</p>
          )}
          {(evidence.data?.failures ?? []).map((f) => (
            <div key={f.id} className="border-b border-border/50 py-2 text-sm last:border-0">
              <div className="font-medium">
                {f.category}: {f.message}
              </div>
              <pre className="mt-1 overflow-auto text-xs text-muted-foreground">
                {JSON.stringify(f.details, null, 2)}
              </pre>
            </div>
          ))}
        </Card>
      )}
    </div>
  );
}

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-xl border border-border/80 p-4">
      <h2 className="mb-3 text-sm font-medium text-muted-foreground">{title}</h2>
      {children}
    </section>
  );
}

function KV({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[8rem_1fr] gap-2 border-b border-border/40 py-2 text-sm last:border-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 break-words">{value}</dd>
    </div>
  );
}
