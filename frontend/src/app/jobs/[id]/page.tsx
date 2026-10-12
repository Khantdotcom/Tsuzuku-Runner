import { AppShell } from "@/components/app-shell";
import { JobDetailView } from "@/components/job-detail";

// Job detail reads the path id and talks to the API; do not prerender it.
export const instant = false;

export default async function JobPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  return (
    <AppShell>
      <JobDetailView id={id} />
    </AppShell>
  );
}
