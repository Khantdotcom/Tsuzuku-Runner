# Backend concepts

Short explanations of the backend ideas Tsuzuku Runner is built on. Each note explains a problem in plain terms, the usual ways to solve it, and where this codebase uses the idea.

These notes explain *why things work the way they do*. For *what the system does*, read [Architecture](../architecture.md) and [Database design](../database-design.md).

| Note | Covers |
| ---- | ------ |
| [Foundations](foundations.md) | Configuration, logging, health checks, graceful shutdown, migrations, connection pools, heartbeats, retries with backoff, token checks, testing against a real database |
| [Consistency](consistency.md) | Transactions, state machines, compare-and-set, idempotency, validation and normalization, append-only history, cursor pagination |
| [Job queue](job-queue.md) | Using a table as a queue, placement versus claiming, row locks, `SKIP LOCKED`, write skew, advisory locks, mutation testing, leases |
| [Long polling](long-polling.md) | Long polling and its alternatives, wake-up signals, the lost wake-up bug, shutting down with open requests, client timeouts |

Read them in the order above; later notes build on earlier ones.
