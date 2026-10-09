import type { NextRequest } from "next/server";

const upstreamTimeoutMs = 5_000;

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
  if (head === "v1" && rest.length > 0) {
    return `/api/v1/${rest.map(encodeURIComponent).join("/")}`;
  }
  return null;
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

export async function GET(
  request: NextRequest,
  ctx: RouteContext<"/api/[...path]">,
): Promise<Response> {
  const { path } = await ctx.params;
  const target = upstreamPath(path);
  if (target === null) {
    return problem(404, "Not Found", "no such API route");
  }

  const url = new URL(target + request.nextUrl.search, apiBaseURL());
  let upstream: Response;
  try {
    upstream = await fetch(url, {
      cache: "no-store",
      headers: { accept: "application/json" },
      signal: AbortSignal.timeout(upstreamTimeoutMs),
    });
  } catch {
    return problem(502, "Bad Gateway", "the Tsuzuku API is unreachable");
  }

  return new Response(upstream.body, {
    status: upstream.status,
    headers: {
      "content-type":
        upstream.headers.get("content-type") ?? "application/json",
      "cache-control": "no-store",
    },
  });
}
