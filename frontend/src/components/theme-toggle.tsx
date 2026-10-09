"use client";

import { useSyncExternalStore } from "react";

import { ThemeTogglerButton } from "@/components/animate-ui/components/buttons/theme-toggler";

const subscribe = () => () => {};

// The stored theme is only known in the browser, so render a placeholder on
// the server to avoid a hydration mismatch on the icon.
export function ThemeToggle() {
  const mounted = useSyncExternalStore(
    subscribe,
    () => true,
    () => false,
  );

  if (!mounted) {
    return <span className="size-9" aria-hidden />;
  }

  return (
    <ThemeTogglerButton
      variant="ghost"
      modes={["light", "dark"]}
      direction="ttb"
      aria-label="Toggle dark mode"
    />
  );
}
