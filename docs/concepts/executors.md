# Executors versus verifiers

Why “the agent said done” is not the same as “the job succeeded”, and how Tsuzuku keeps those roles separate.

## The problem

An AI agent (or any long-running worker) can exit zero after changing nothing useful, deleting tests, or lying in a summary. If the platform trusted that exit code alone, bad outcomes would look like success.

## The idea

Split **execution** from **judgment**:

1. An **executor** produces changes in a workspace (shell command, or an AI CLI).
2. An independent **verifier** judges the result (exit code, verification commands, acceptance checks, integrity heuristics).

Swapping how work is done must not bypass verification.

## In this project

`worker.JobRunner` is the seam. `*Executor` is the command executor (checkout → `sh -c command` → verify). `AIExecutor` expands `TSUZUKU_AI_EXECUTOR_COMMAND` and reuses the same path with the expanded command as the execute step. `SelectingRunner` picks by `spec.executor.kind` (`command` default, or `ai` when the worker has `TSUZUKU_AI_EXECUTOR_ENABLED=true`). AI workloads must include verification. See [Logs, evidence, and cancellation](evidence.md#verification-independent-of-the-executor).
