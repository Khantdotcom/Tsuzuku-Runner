"use client";

import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";

import { APIError, submitWorkload, type WorkloadRequest } from "@/lib/api";

type Mode = "now" | "schedule";

export function SubmitForm() {
  const router = useRouter();
  const [mode, setMode] = useState<Mode>("now");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setPending(true);
    const fd = new FormData(e.currentTarget);
    const criteriaRaw = String(fd.get("acceptance_criteria") ?? "");
    const criteria = criteriaRaw
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);

    const body: WorkloadRequest = {
      repository: String(fd.get("repository") ?? "").trim(),
      revision: String(fd.get("revision") ?? "").trim(),
      command: String(fd.get("command") ?? "").trim(),
      acceptance_criteria: criteria.length ? criteria : undefined,
      resources: {
        cpu: Number(fd.get("cpu") || 1),
        memory_mb: Number(fd.get("memory_mb") || 1024),
      },
      timeout_seconds: Number(fd.get("timeout_seconds") || 600),
      priority: Number(fd.get("priority") || 0),
      runtime: {
        image: String(fd.get("image") || "golang:1.27").trim(),
        network: fd.get("network") === "on",
      },
    };
    const verification = String(fd.get("verification") ?? "").trim();
    if (verification) {
      body.verification = { command: verification };
    }
    const executorKind = String(fd.get("executor_kind") ?? "command").trim();
    const executorPrompt = String(fd.get("executor_prompt") ?? "").trim();
    if (executorKind === "ai" || executorPrompt) {
      body.executor = {
        kind: executorKind === "ai" ? "ai" : "command",
        prompt: executorPrompt || undefined,
      };
    }
    if (fd.get("github_pr") === "on") {
      body.delivery = { github_pr: true };
    }
    if (mode === "schedule") {
      const when = String(fd.get("scheduled_at") ?? "");
      if (!when) {
        setError("Pick a date and time to schedule");
        setPending(false);
        return;
      }
      body.scheduled_at = new Date(when).toISOString();
    }

    try {
      const job = await submitWorkload(body, crypto.randomUUID());
      router.push(`/jobs/${job.number}`);
    } catch (err) {
      setError(err instanceof APIError ? err.message : "Could not submit workload");
      setPending(false);
    }
  }

  return (
    <form onSubmit={onSubmit} className="mx-auto max-w-2xl space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Submit workload</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Run a command against a public repository, optionally verify it, then
          inspect the evidence.
        </p>
      </div>

      <fieldset className="space-y-4 rounded-xl border border-border/80 p-4">
        <legend className="px-1 text-sm font-medium">Source</legend>
        <Field label="Repository" name="repository" required placeholder="https://github.com/octocat/Hello-World" />
        <Field label="Revision" name="revision" required defaultValue="master" />
        <Field
          label="Command"
          name="command"
          required
          as="textarea"
          defaultValue="test -f README"
        />
        <Field
          label="Verification command"
          name="verification"
          as="textarea"
          placeholder="grep -q Hello README"
          hint="Optional. Runs after a successful command exit."
        />
        <Field
          label="Acceptance criteria"
          name="acceptance_criteria"
          as="textarea"
          hint="One per line. Used as the AI prompt when executor is ai and no prompt is set."
        />
        <label className="block text-sm">
          <span className="font-medium">Executor</span>
          <select
            name="executor_kind"
            defaultValue="command"
            className="mt-1 w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none ring-ring focus:ring-2"
          >
            <option value="command">Command (shell)</option>
            <option value="ai">AI CLI (requires verification)</option>
          </select>
          <span className="mt-1 block text-xs text-muted-foreground">
            AI runs a worker-configured CLI template; verification still decides the outcome.
          </span>
        </label>
        <Field
          label="AI prompt"
          name="executor_prompt"
          as="textarea"
          placeholder="Optional override when executor is AI"
        />
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" name="github_pr" className="size-4 rounded border" />
          Open a GitHub PR with the patch after COMPLETED (needs server token)
        </label>
      </fieldset>

      <fieldset className="space-y-4 rounded-xl border border-border/80 p-4">
        <legend className="px-1 text-sm font-medium">Resources</legend>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <Field label="CPU (cores)" name="cpu" type="number" step="0.1" defaultValue="1" />
          <Field label="Memory (MB)" name="memory_mb" type="number" defaultValue="1024" />
          <Field label="Timeout (s)" name="timeout_seconds" type="number" defaultValue="600" />
          <Field
            label="Priority"
            name="priority"
            type="number"
            min={0}
            max={100}
            defaultValue="0"
            hint="0–100; higher jobs are placed first"
          />
        </div>
        <Field label="Image" name="image" defaultValue="golang:1.27" />
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" name="network" defaultChecked className="size-4 rounded border" />
          Allow network in the container
        </label>
      </fieldset>

      <fieldset className="space-y-3 rounded-xl border border-border/80 p-4">
        <legend className="px-1 text-sm font-medium">When</legend>
        <div className="flex gap-2">
          <ModeButton active={mode === "now"} onClick={() => setMode("now")}>
            Run now
          </ModeButton>
          <ModeButton active={mode === "schedule"} onClick={() => setMode("schedule")}>
            Schedule
          </ModeButton>
        </div>
        {mode === "schedule" && (
          <Field label="Scheduled at" name="scheduled_at" type="datetime-local" required />
        )}
      </fieldset>

      {error && (
        <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {error}
        </p>
      )}

      <button
        type="submit"
        disabled={pending}
        className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-60"
      >
        {pending ? "Submitting…" : mode === "now" ? "Run now" : "Schedule job"}
      </button>
    </form>
  );
}

function ModeButton({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        active
          ? "rounded-md bg-muted px-3 py-1.5 text-sm font-medium"
          : "rounded-md px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground"
      }
    >
      {children}
    </button>
  );
}

function Field({
  label,
  name,
  hint,
  as = "input",
  ...props
}: {
  label: string;
  name: string;
  hint?: string;
  as?: "input" | "textarea";
} & React.InputHTMLAttributes<HTMLInputElement> &
  React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  const className =
    "mt-1 w-full rounded-md border border-input bg-background px-3 py-2 text-sm outline-none ring-ring focus:ring-2";
  return (
    <label className="block text-sm">
      <span className="font-medium">{label}</span>
      {as === "textarea" ? (
        <textarea name={name} rows={3} className={className} {...props} />
      ) : (
        <input name={name} className={className} {...props} />
      )}
      {hint && <span className="mt-1 block text-xs text-muted-foreground">{hint}</span>}
    </label>
  );
}
