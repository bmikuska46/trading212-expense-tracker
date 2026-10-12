import type { NextConfig } from "next";
import { PHASE_DEVELOPMENT_SERVER } from "next/constants";

const nextConfig = {
  output: "export",
  trailingSlash: true,
  images: { unoptimized: true },
} satisfies NextConfig;

// The dev server has no static export. It hot reloads pages and proxies the
// API to the Go server, so the browser sees one origin as in production. API
// paths have no trailing slash, so the slash redirect is off in development.
const devConfig = {
  trailingSlash: true,
  skipTrailingSlashRedirect: true,
  agentRules: false,
  images: { unoptimized: true },
  rewrites: async () => [
    { source: "/api/:path*", destination: `${process.env.APP_API_URL ?? "http://127.0.0.1:8080"}/api/:path*` },
  ],
} satisfies NextConfig;

export default function config(phase: string): NextConfig {
  return phase === PHASE_DEVELOPMENT_SERVER ? devConfig : nextConfig;
}
