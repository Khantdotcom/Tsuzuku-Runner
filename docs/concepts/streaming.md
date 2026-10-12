# Live updates and scheduled work

How the dashboard watches a job without flooding the server, and how a job can wait until a chosen time before it is placed.

## Server-sent events

**The problem.** A job's timeline and logs change for minutes. Asking for the latest page every half-second works, but wastes requests when nothing changed, and is still late when something did.

**The idea.** **Server-sent events (SSE)** keep one HTTP response open. The server writes `event:` / `data:` frames as facts appear. The browser's `EventSource` reconnects automatically and sends `Last-Event-ID`, so a dropped connection resumes from the last seen item.

Unlike WebSockets, SSE is one-way (server to client) and rides ordinary HTTP, so proxies and the frontend's allowlisted BFF can forward it without a special upgrade protocol.

**In this project.** `GET /api/v1/jobs/{id}/events/stream` and `.../logs/stream` poll the same queries as the paged list endpoints every second, emit numbered frames, and send a final `done` event once the job is terminal and there is nothing left to drain. Reconnect with `?after=` or `Last-Event-ID`. The Next.js `/api` proxy allows these streams a long timeout so they are not cut after five seconds.

## Scheduled placement

**The problem.** Sometimes a workload should not start immediately: wait for an off-peak window, or for a human to be around.

**The idea.** Store a **not-before** time on the job. The scheduler only places jobs whose time is empty or already past. Until then the job stays `QUEUED` and can still be cancelled.

**In this project.** `POST /workloads` accepts optional `scheduled_at` (RFC 3339). Times more than five seconds in the past are rejected so clock skew does not lock a job forever. The column was already on `jobs`; submit now fills it. `LockQueuedJobs` already skips future times.

## Dashboard counts

**The idea.** Counting every job by raw state is noisy for a status strip. Group in-flight states into one **running** bucket (preparing, executing, verifying) and keep the rest as their own totals.

**In this project.** `GET /api/v1/stats` uses `CountJobsByState` and returns `queued`, `scheduled`, `running`, `completed`, `failed`, and `cancelled`.
