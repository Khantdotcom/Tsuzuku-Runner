import { ThemeToggle } from "@/components/theme-toggle";

export function SiteHeader() {
  return (
    <header className="sticky top-0 z-40 border-b border-border/60 bg-background/70 backdrop-blur-xl">
      <div className="mx-auto flex h-14 max-w-6xl items-center justify-between px-6">
        <div className="flex items-center gap-2.5">
          <span className="grid size-8 place-items-center rounded-lg bg-gradient-to-br from-indigo-500 via-violet-500 to-fuchsia-500 text-sm font-semibold text-white shadow-[0_0_20px_-2px_var(--glow)]">
            続
          </span>
          <span className="font-semibold tracking-tight">Tsuzuku Runner</span>
        </div>
        <ThemeToggle />
      </div>
    </header>
  );
}
