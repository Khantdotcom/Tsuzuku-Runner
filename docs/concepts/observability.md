# Observability

How operators see whether the platform is healthy without reading every log line.

## Metrics

**The problem.** Logs tell stories about single requests. Operators need totals and trends: how deep is the queue, how fast is the API, how often do jobs fail.

**The idea.** Expose **metrics** in a format scrapers understand (Prometheus), then graph them. Counters only go up; gauges are current values; histograms show latency distributions.

**In this project.** The API serves `GET /metrics`. HTTP middleware records request count and latency by method, route, and status. Job submits increment `tsuzuku_jobs_total{event="submit"}`. `GET /stats` refreshes `tsuzuku_queue_depth` gauges. The scheduler records tick duration and placements. Run the local stack with `docker compose --profile obs up` for Prometheus (`:9090`) and Grafana (`:3001`, admin/`tsuzuku`).

## Traces (optional later)

Distributed traces follow one request across services. Tsuzuku can add OTLP export later via `TSUZUKU_OTEL_ENDPOINT`; metrics and structured logs (`job_id`, `attempt_id`, chi `request_id`) are the current correlation tools.
