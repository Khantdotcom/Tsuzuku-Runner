import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Standalone output is for the Compose/Docker image only. Vercel uses the
  // default Next.js build; DOCKER_BUILD=1 is set in frontend/Dockerfile.
  ...(process.env.DOCKER_BUILD === "1" ? { output: "standalone" as const } : {}),
  cacheComponents: true,
  partialPrefetching: true,
  turbopack: {
    rules: {
      "*.css": {
        loaders: ["@tailwindcss/turbopack"],
        as: "*.css",
      },
    },
  },
};

export default nextConfig;
