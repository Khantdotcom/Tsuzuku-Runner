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

export class APIError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = "APIError";
  }
}

export async function probe(name: ProbeName): Promise<ProbeResult> {
  const res = await fetch(`/api/${name}`, { cache: "no-store" });
  return { ok: res.ok, status: res.status };
}

export async function listWorkers(): Promise<Worker[]> {
  const res = await fetch("/api/v1/workers", { cache: "no-store" });
  if (!res.ok) {
    throw new APIError(res.status, await problemDetail(res));
  }
  const body = (await res.json()) as { workers: Worker[] };
  return body.workers;
}

async function problemDetail(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { detail?: string; title?: string };
    return body.detail || body.title || res.statusText;
  } catch {
    return res.statusText;
  }
}
