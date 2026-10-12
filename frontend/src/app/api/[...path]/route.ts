import type { NextRequest } from "next/server";

const upstreamTimeoutMs = 5_000;
const downloadTimeoutMs = 8_000;
// Kept for local Compose; the Hobby UI polls JSON APIs instead of SSE.
const streamTimeoutMs = 15 * 60_000;

function apiBaseURL(): string {
  return process.env.TSUZUKU_API_URL ?? "http://localhost:8080";
}

// Maps a browser path under /api to an API server path. Anything else is
// rejected, so the proxy cannot reach routes the UI does not use.
function upstreamPath(segments: string[]): string | null {
  if (segments.some((s) => s === "" || s === "." || s === "..")) {
    return null;
  }
  const [head, ...rest] = segments;
  if (rest.length === 0 && (head === "healthz" || head === "readyz")) {
    return `/${head}`;
  }
  if (head === "openapi.yaml" && rest.length === 0) {
    return "/openapi.yaml";
  }
  if (head === "v1" && rest.length > 0) {
    return `/api/v1/${rest.map(encodeURIComponent).join("/")}`;
  }
  return null;
}

function isStreamPath(segments: string[]): boolean {
  return (
    segments[0] === "v1" &&
    segments[1] === "jobs" &&
    segments.length >= 4 &&
    (segments[3] === "events" || segments[3] === "logs") &&
    segments[4] === "stream"
  );
}

function isDownloadPath(segments: string[]): boolean {
  if (segments[0] !== "v1" || segments[1] !== "jobs") {
    return false;
  }
  // /v1/jobs/{id}/artifacts/{artifactID} or .../evidence/bundle
  if (segments.length === 5 && segments[3] === "artifacts") {
    return true;
  }
  if (segments.length === 5 && segments[3] === "evidence" && segments[4] === "bundle") {
    return true;
  }
  return false;
}

function problem(status: number, title: string, detail: string): Response {
  return Response.json(
    { type: "about:blank", title, status, detail },
    {
      status,
      headers: {
        "content-type": "application/problem+json",
        "cache-control": "no-store",
      },
    },
  );
}

function timeoutFor(segments: string[]): number {
  if (isStreamPath(segments)) {
    return streamTimeoutMs;
  }
  if (isDownloadPath(segments)) {
    return downloadTimeoutMs;
  }
  return upstreamTimeoutMs;
}

async function proxy(
  request: NextRequest,
  ctx: RouteContext<"/api/[...path]">,
): Promise<Response> {
  const { path } = await ctx.params;
  const target = upstreamPath(path);
  if (target === null) {
    return problem(404, "Not Found", "no such API route");
  }

  const stream = isStreamPath(path);
  const url = new URL(target + request.nextUrl.search, apiBaseURL());
  const headers = new Headers();
  const accept = request.headers.get("accept");
  if (accept) {
    headers.set("accept", accept);
  } else if (stream) {
    headers.set("accept", "text/event-stream");
  } else {
    headers.set("accept", "application/json");
  }
  const contentType = request.headers.get("content-type");
  if (contentType) {
    headers.set("content-type", contentType);
  }
  const idempotency = request.headers.get("idempotency-key");
  if (idempotency) {
    headers.set("idempotency-key", idempotency);
  }
  const lastEventID = request.headers.get("last-event-id");
  if (lastEventID) {
    headers.set("last-event-id", lastEventID);
  }

  let upstream: Response;
  try {
    upstream = await fetch(url, {
      method: request.method,
      cache: "no-store",
      headers,
      body:
        request.method === "GET" || request.method === "HEAD"
          ? undefined
          : await request.arrayBuffer(),
      signal: AbortSignal.timeout(timeoutFor(path)),
    });
  } catch {
    return problem(502, "Bad Gateway", "the Tsuzuku API is unreachable");
  }

  const out = new Headers();
  const contentTypeOut =
    upstream.headers.get("content-type") ??
    (stream ? "text/event-stream" : "application/json");
  out.set("content-type", contentTypeOut);
  out.set("cache-control", "no-store");
  if (stream) {
    out.set("connection", "keep-alive");
    out.set("x-accel-buffering", "no");
  }
  for (const name of [
    "location",
    "idempotent-replayed",
    "etag",
    "content-disposition",
    "content-length",
  ]) {
    const value = upstream.headers.get(name);
    if (value) {
      out.set(name, value);
    }
  }

  return new Response(upstream.body, {
    status: upstream.status,
    headers: out,
  });
}

export async function GET(
  request: NextRequest,
  ctx: RouteContext<"/api/[...path]">,
): Promise<Response> {
  return proxy(request, ctx);
}

export async function POST(
  request: NextRequest,
  ctx: RouteContext<"/api/[...path]">,
): Promise<Response> {
  return proxy(request, ctx);
}
