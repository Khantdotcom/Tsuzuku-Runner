# Tsuzuku Runner 🏃‍♂️

Tsuzuku (続く) in Japanese means "to continue" or "to keep on".

Tsuzuku Runner is an execution engine designed to manage, verify, and recover autonomous software workloads.

## Industry Problems

1. Agent Finished ≠ Software Is Correct: Agents are self-reporting thinking they did things right.

2. Dumb Retries Burn Money: Standard retry loops throw expensive tokens.

3. Resource Blindness: Agents aren't aware of real-time CPU, RAM, and infrastructure capacity.

## Core Focus

Survive runtime failures, retain execution context, and guarantee verifiable outputs.

## The Stack

Backend: Go (modular monolith)

State: PostgreSQL (transactional transitions, strict idempotency)

Compute: Docker / Linux VMs

Observability: OpenTelemetry, Prometheus, Grafana
