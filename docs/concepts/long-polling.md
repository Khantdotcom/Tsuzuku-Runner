# Long polling

How a worker finds out about new work within milliseconds over plain HTTP, and the timing bugs to avoid when one goroutine waits for a signal from another.

## The problem

A worker wants to start a job as soon as one is assigned. The options:

- **Short polling:** ask every N seconds. It is simple, but you choose between slow (a large N) and wasteful (a small N means thousands of empty requests).
- **Long polling:** ask once, and the server *holds the request open* until there is something to return or a time limit passes. Then the client asks again.
- **WebSockets:** a permanent two-way connection.
- **Server-sent events (SSE):** a permanent one-way stream from server to client.

## Why long polling here

Long polling is ordinary HTTP: the same authentication, the same error format, nothing special for proxies and load balancers to understand. Each request stands alone, so a server restart or a dropped connection needs no reconnect protocol; the worker simply sends the next request.

WebSockets and SSE pay off when there is a constant stream of messages. A worker receives one job at a time, so a held request gives the same speed with fewer moving parts.

**In this project.** `POST /api/v1/workers/{id}/claim?wait=25s` returns `200` with a job, or `204 No Content` if nothing was assigned within the wait (at most 30 seconds). Without `wait`, it answers immediately.

## Waking up waiting requests

**The problem.** A claim is waiting. The scheduler, running in the same process, places a job on that worker. How does the waiting request find out?

Checking the database every few milliseconds would be wasteful. Instead, the scheduler *signals* waiting requests after it commits, and they check the database once.

**Close-to-broadcast.** In Go, closing a channel wakes *every* goroutine waiting on it, at once. The notifier keeps one channel at a time:

- `Wait()` returns the current channel.
- `Broadcast()` closes it, waking everyone, and puts a fresh channel in its place for the next round.

The notifier does not track who is waiting, and a burst of broadcasts never blocks.

**In this project.** `scheduler.Notifier`. A request typically wakes within a few milliseconds of the scheduler committing a placement.

**The safety net.** The signal only reaches requests in the *same* process. With several server replicas, the scheduler might run in another one. Waiting claims therefore also re-check the database every 2 seconds. The signal makes it fast; the timer keeps it correct.

## The lost wake-up bug

The order of two lines in the claim handler decides whether it is correct.

**The wrong order:**

1. Check the database for an assigned job; find none.
2. *(The scheduler places a job and broadcasts. Nobody is listening yet.)*
3. Get the wake-up channel and wait on it.

The broadcast in step 2 happened before the handler started listening, so it was missed. The handler sleeps until the next timer re-check even though a job is waiting. This is a **lost wake-up**, a classic concurrency bug. It cannot be found by reading either step on its own, only by thinking about what can happen *between* them.

**The right order:**

1. Get the wake-up channel.
2. Check the database; find none.
3. Wait on the channel from step 1.

Now any broadcast after step 1 closes a channel we already hold, so step 3 returns immediately. "Subscribe first, then check" is the general rule wherever you wait for a signal about some state.

## Graceful shutdown with open requests

**The problem.** On shutdown, Go's `http.Server.Shutdown` stops accepting new requests and *waits for open ones to finish*. A claim waiting up to 30 seconds would delay every shutdown, or be cut off when the shutdown timeout runs out.

**The idea.** Tell long-lived requests to give up early. `srv.RegisterOnShutdown` runs a function the moment shutdown begins; ours closes a `stopping` channel. Every waiting claim also waits on that channel, so it returns `204` straight away. To the worker this is an ordinary "no job yet", and it simply asks again, reaching whichever server is up.

**In this project.** Stopping the server during a 30-second claim takes well under a second, and the claim receives `204`.

## Every way out of the wait

A waiting claim ends in exactly one of these ways, and each one is handled:

| What happened | Response |
| ------------- | -------- |
| A job was assigned (signal or timer re-check) | `200` with the job |
| The wait ran out | `204` |
| The server is shutting down | `204` |
| The worker disconnected | nothing is sent; the handler just returns |
| The database failed | `500` |

When the client disconnects, the request's context is cancelled. Code that waits must always include `<-ctx.Done()` in its `select`, or abandoned requests keep goroutines and timers alive.

## Client timeouts for long requests

**The problem.** The worker's HTTP client has a 10-second timeout, which is right for registration and heartbeats. Applied to a 25-second long poll, the client would give up on its own request every time.

**The idea.** Use a separate client with no global timeout for long polls, and give each request a deadline through its context: the wait time plus a margin (10 seconds) for the server to finish answering. Every request still has a limit; it is just the right limit for that request.

**In this project.** `worker.Client.Claim` uses a dedicated long-poll client. `TestClientClaimOutlivesShortRequestTimeout` confirms that a claim can last longer than the normal request timeout.

## Not holding resources while waiting

A waiting claim holds no database connection and no transaction. It checks the database briefly, releases the connection, and then waits on channels. Holding a pooled connection for 30 seconds per waiting worker would let a handful of idle workers use up the whole pool. See [Connection pools](foundations.md#connection-pools).
