import type { ReactNode } from "react";

import { GlowingEffect } from "@/components/ui/glowing-effect";
import { cn } from "@/lib/utils";

export function GlowCard({
  className,
  children,
}: {
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={cn(
        "relative h-full rounded-2xl border border-border/70 p-1.5",
        className,
      )}
    >
      <GlowingEffect
        spread={40}
        glow
        disabled={false}
        proximity={64}
        inactiveZone={0.01}
        borderWidth={2}
      />
      <div className="relative h-full rounded-xl border border-border/60 bg-card p-5 shadow-[0_0_32px_-14px_var(--glow)] backdrop-blur-md">
        {children}
      </div>
    </div>
  );
}
