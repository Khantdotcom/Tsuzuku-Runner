import type { JobState } from "@/lib/api";
import { cn } from "@/lib/utils";

const tones: Record<JobState, string> = {
  QUEUED: "border-border bg-muted text-muted-foreground",
  SCHEDULED: "border-border bg-muted text-muted-foreground",
  PREPARING: "border-primary/25 bg-primary/10 text-primary",
  EXECUTING: "border-primary/25 bg-primary/10 text-primary",
  VERIFYING: "border-primary/25 bg-primary/10 text-primary",
  COMPLETED: "border-success/30 bg-success/10 text-success",
  FAILED: "border-destructive/30 bg-destructive/10 text-destructive",
  CANCELLED: "border-border bg-muted text-muted-foreground",
  RETRYING: "border-primary/25 bg-primary/10 text-primary",
  REPAIRING: "border-primary/25 bg-primary/10 text-primary",
  BLOCKED: "border-destructive/30 bg-destructive/10 text-destructive",
};

export function JobStateBadge({ state }: { state: JobState }) {
  return (
    <span
      className={cn(
        "inline-flex rounded-md border px-2 py-0.5 text-xs font-medium tracking-wide",
        tones[state] ?? tones.QUEUED,
      )}
    >
      {state}
    </span>
  );
}
