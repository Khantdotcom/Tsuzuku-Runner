import { SiteHeader } from "@/components/site-header";
import { SystemStatus } from "@/components/system-status";
import { AuroraBackground } from "@/components/ui/aurora-background";

export default function Home() {
  return (
    <>
      <SiteHeader />
      <main className="flex-1">
        <AuroraBackground className="h-auto bg-background pt-20 pb-28 text-foreground sm:pt-28 sm:pb-36 dark:bg-background">
          <div className="relative z-10 mx-auto max-w-3xl px-6 text-center">
            <span className="inline-flex items-center rounded-full border border-primary/25 bg-primary/10 px-3 py-1 text-xs font-medium text-primary">
              v0.1 · Foundation
            </span>
            <h1 className="mt-6 text-4xl font-semibold tracking-tight sm:text-6xl">
              Execution that{" "}
              <span className="bg-gradient-to-r from-indigo-500 via-violet-500 to-fuchsia-500 bg-clip-text text-transparent drop-shadow-[0_0_24px_var(--glow)]">
                continues
              </span>
            </h1>
            <p className="mx-auto mt-5 max-w-xl text-lg text-muted-foreground">
              Tsuzuku (続く) runs software workloads on a fleet of workers,
              verifies the results independently, and keeps going when
              something fails.
            </p>
          </div>
        </AuroraBackground>

        <section className="relative z-10 mx-auto -mt-14 max-w-6xl px-6 pb-24">
          <SystemStatus />
        </section>
      </main>
    </>
  );
}
