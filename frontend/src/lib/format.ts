export function formatMemory(mb: number): string {
  if (mb >= 1024) {
    return `${(mb / 1024).toFixed(1)} GB`;
  }
  return `${mb} MB`;
}

export function formatCores(cpuMillis: number): string {
  const cores = cpuMillis / 1000;
  return `${Number.isInteger(cores) ? cores : cores.toFixed(1)} ${cores === 1 ? "core" : "cores"}`;
}

export function formatAgo(iso: string, now: number): string {
  const seconds = Math.max(0, Math.round((now - Date.parse(iso)) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

export function relativeTime(iso: string, nowMs: number): string {
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return iso;
  const delta = Math.round((nowMs - then) / 1000);
  if (delta < 5) return "just now";
  return formatAgo(iso, nowMs);
}

export function durationBetween(
  start: string | null,
  end: string | null,
  nowMs: number,
): string {
  if (!start) return "—";
  const from = Date.parse(start);
  const to = end ? Date.parse(end) : nowMs;
  if (Number.isNaN(from) || Number.isNaN(to)) return "—";
  const ms = Math.max(0, to - from);
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const rem = sec % 60;
  if (min < 60) return `${min}m ${rem}s`;
  const hr = Math.floor(min / 60);
  return `${hr}h ${min % 60}m`;
}

export function shortRepo(url: string): string {
  try {
    const u = new URL(url);
    return u.pathname.replace(/^\//, "").replace(/\.git$/, "") || u.host;
  } catch {
    return url;
  }
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}
