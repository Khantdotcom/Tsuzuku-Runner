// Client for the Tsuzuku API. Requests go through the frontend's own /api
// proxy (src/app/api/[...path]/route.ts), so the browser never needs CORS.

export type WorkerStatus = "online" | "offline";

export interface WorkerMetadata {
  os?: string;
  arch?: string;
  hostname?: string;
  version?: string;
}

export interface Worker {
  id: string;
  name: string;
  status: WorkerStatus;
  slots: number;
  cpu_millis: number;
  memory_mb: number;
  active_jobs: number;
  reserved_cpu_millis: number;
  reserved_memory_mb: number;
  cpu_used_percent: number | null;
  memory_used_mb: number | null;
  metadata: WorkerMetadata;
  registered_at: string;
  last_heartbeat_at: string;
}

export type ProbeName = "healthz" | "readyz";

export interface ProbeResult {
  ok: boolean;
  status: number;
}

export type JobState =
  | "QUEUED"
  | "SCHEDULED"
  | "PREPARING"
  | "EXECUTING"
  | "VERIFYING"
  | "COMPLETED"
  | "FAILED"
  | "RETRYING"
  | "REPAIRING"
  | "BLOCKED"
  | "CANCELLED";

export interface Stats {
  queued: number;
  scheduled: number;
  running: number;
  completed: number;
  failed: number;
  cancelled: number;
}

export interface Job {
  id: string;
  number: number;
  state: JobState;
  priority: number;
  assigned_worker_id: string | null;
  scheduled_at: string | null;
  cancel_requested_at: string | null;
  created_at: string;
  updated_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface QueueJob {
  id: string;
  number: number;
  priority: number;
  cpu_millis: number;
  memory_mb: number;
  image: string;
  repository: string;
  revision: string;
  command: string;
  scheduled_at: string | null;
  created_at: string;
}

export interface WorkloadSummary {
  repository: string;
  revision: string;
  command: string;
  image: string;
}

export interface JobListItem extends Job {
  workload: WorkloadSummary;
}

export interface JobDetail extends Job {
  workload: {
    id: string;
    spec: Record<string, unknown>;
    created_at: string;
  };
  transitions: Array<{
    from: string | null;
    to: string;
    actor: string;
    reason: string;
    at: string;
  }>;
}

export interface Attempt {
  id: string;
  attempt_number: number;
  worker_id: string;
  status: string;
  exit_code: number | null;
  error: string | null;
  started_at: string;
  finished_at: string | null;
}

export interface JobEvent {
  id: number;
  type: string;
  attempt_id: string | null;
  payload: Record<string, unknown>;
  created_at: string;
}

export interface LogChunk {
  id: number;
  attempt_id: string;
  seq: number;
  stream: "stdout" | "stderr" | string;
  data: string;
  created_at: string;
}

export interface Artifact {
  id: string;
  attempt_id: string | null;
  name: string;
  content_type: string;
  size_bytes: number;
  sha256: string;
  created_at: string;
  url: string;
}

export interface VerificationCheck {
  name: string;
  kind: string;
  command: string | null;
  status: string;
  exit_code: number | null;
  output: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Verification {
  id: string;
  attempt_id: string;
  status: string;
  started_at: string;
  finished_at: string | null;
  checks: VerificationCheck[];
}

export interface Failure {
  id: string;
  attempt_id: string | null;
  category: string;
  message: string;
  details: Record<string, unknown>;
  created_at: string;
}

export interface Evidence {
  artifacts: Artifact[];
  verifications: Verification[];
  failures: Failure[];
}

export interface WorkloadRequest {
  repository: string;
  revision: string;
  command: string;
  acceptance_criteria?: string[];
  resources?: { cpu?: number; memory_mb?: number };
  timeout_seconds?: number;
  runtime?: { image?: string; network?: boolean };
  verification?: { command: string };
  executor?: { kind?: string; prompt?: string };
  delivery?: { github_pr?: boolean };
  scheduled_at?: string;
  priority?: number;
}

export interface Delivery {
  id: string;
  kind: string;
  status: string;
  idempotency_key: string;
  target: string;
  external_ref: string | null;
  created_at: string;
  updated_at: string;
}

export class APIError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "APIError";
  }
}

async function problemDetail(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { detail?: string; title?: string };
    return body.detail || body.title || res.statusText;
  } catch {
    return res.statusText;
  }
}

async function getJSON<T>(path: string): Promise<T> {
  const res = await fetch(`/api${path}`, { cache: "no-store" });
  if (!res.ok) {
    throw new APIError(res.status, await problemDetail(res));
  }
  return res.json() as Promise<T>;
}

export async function probe(name: ProbeName): Promise<ProbeResult> {
  const res = await fetch(`/api/${name}`, { cache: "no-store" });
  return { ok: res.ok, status: res.status };
}

export async function getStats(): Promise<Stats> {
  return getJSON<Stats>("/v1/stats");
}

export async function listQueue(limit = 50): Promise<QueueJob[]> {
  const body = await getJSON<{ jobs: QueueJob[] }>(`/v1/queue?limit=${limit}`);
  return body.jobs;
}

export async function listWorkers(): Promise<Worker[]> {
  const body = await getJSON<{ workers: Worker[] }>("/v1/workers");
  return body.workers;
}

export async function listJobs(params?: {
  state?: JobState;
  before?: number;
  limit?: number;
}): Promise<{ jobs: JobListItem[]; next_before: number | null }> {
  const q = new URLSearchParams();
  if (params?.state) q.set("state", params.state);
  if (params?.before != null) q.set("before", String(params.before));
  if (params?.limit != null) q.set("limit", String(params.limit));
  const suffix = q.size ? `?${q}` : "";
  return getJSON(`/v1/jobs${suffix}`);
}

export async function getJob(id: string): Promise<JobDetail> {
  return getJSON<JobDetail>(`/v1/jobs/${encodeURIComponent(id)}`);
}

export async function listAttempts(id: string): Promise<Attempt[]> {
  const body = await getJSON<{ attempts: Attempt[] }>(
    `/v1/jobs/${encodeURIComponent(id)}/attempts`,
  );
  return body.attempts;
}

export async function getEvidence(id: string): Promise<Evidence> {
  return getJSON<Evidence>(`/v1/jobs/${encodeURIComponent(id)}/evidence`);
}

export async function listDeliveries(id: string): Promise<Delivery[]> {
  const body = await getJSON<{ deliveries: Delivery[] }>(
    `/v1/jobs/${encodeURIComponent(id)}/deliveries`,
  );
  return body.deliveries;
}

export async function retryDeliveries(id: string): Promise<Delivery[]> {
  const res = await fetch(`/api/v1/jobs/${encodeURIComponent(id)}/deliveries`, {
    method: "POST",
    headers: { accept: "application/json" },
    cache: "no-store",
  });
  if (!res.ok) {
    throw new APIError(res.status, await problemDetail(res));
  }
  const body = (await res.json()) as { deliveries: Delivery[] };
  return body.deliveries;
}

export async function submitWorkload(
  body: WorkloadRequest,
  idempotencyKey?: string,
): Promise<JobDetail> {
  const headers: Record<string, string> = {
    "content-type": "application/json",
    accept: "application/json",
  };
  if (idempotencyKey) {
    headers["idempotency-key"] = idempotencyKey;
  }
  const res = await fetch("/api/v1/workloads", {
    method: "POST",
    headers,
    body: JSON.stringify(body),
    cache: "no-store",
  });
  if (!res.ok) {
    throw new APIError(res.status, await problemDetail(res));
  }
  return res.json() as Promise<JobDetail>;
}

export async function cancelJob(id: string): Promise<Job> {
  const res = await fetch(`/api/v1/jobs/${encodeURIComponent(id)}/cancel`, {
    method: "POST",
    headers: { accept: "application/json" },
    cache: "no-store",
  });
  if (!res.ok) {
    throw new APIError(res.status, await problemDetail(res));
  }
  return res.json() as Promise<Job>;
}

/** Decode a base64 log chunk payload from the JSON API into text. */
export function decodeLogData(data: string): string {
  if (typeof atob === "function") {
    const binary = atob(data);
    const bytes = Uint8Array.from(binary, (c) => c.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  }
  return Buffer.from(data, "base64").toString("utf8");
}

export function isTerminalState(state: JobState): boolean {
  return state === "COMPLETED" || state === "FAILED" || state === "CANCELLED";
}

export function isCancellableState(state: JobState): boolean {
  return (
    state === "QUEUED" ||
    state === "SCHEDULED" ||
    state === "PREPARING" ||
    state === "EXECUTING" ||
    state === "VERIFYING"
  );
}
