# Outbound delivery

Why a completed job still needs a separate, idempotent step to publish its result.

**The problem.** Verification and evidence prove what happened inside Tsuzuku. Callers also want the change somewhere else: a downloadable patch, a GitHub pull request, later a deployment or ticket update. Those calls are slow, can fail, and must not run twice if the worker or server retries.

**The idea.** Treat delivery as an **outbound adapter** with its own row and a **stable idempotency key**. Insert or look up the row, claim it into `IN_PROGRESS`, perform the side effect outside the job-finish transaction, then mark `DELIVERED` or `FAILED`. A second attempt with the same key returns the existing success instead of opening another PR.

**In this project.** The `deliveries` table already allowed kinds `PATCH`, `GITHUB_PR`, and others. After a job reaches `COMPLETED`, `delivery.Service.Deliver` runs best effort (same pattern as evidence archival: after commit, detached timeout).

- **PATCH** — when a `diff.patch` artifact exists, a row is recorded with `external_ref` pointing at the artifact download URL. Idempotency key `job:<uuid>:PATCH`.
- **GITHUB_PR** — only when the workload sets `delivery.github_pr` and the server has `TSUZUKU_GITHUB_TOKEN`. The adapter commits `tsuzuku/job-<n>.patch` on branch `tsuzuku/job-<n>` via the GitHub Git Data API (no `git` binary on the distroless server) and opens or reuses a pull request. Key `job:<uuid>:GITHUB_PR`. `TSUZUKU_GITHUB_API_BASE` defaults to `https://api.github.com`.

`GET /api/v1/jobs/{id}/deliveries` lists rows; `POST` retries for `COMPLETED` jobs. See [Architecture](../architecture.md) and [Database design](../database-design.md).
