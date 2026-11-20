import type { NextConfig } from "next";

// The browser only talks to this origin; /api/v1/* is proxied to the Go API
// so the session cookie stays first-party. API_URL is read at build/start.
const apiURL = process.env.API_URL ?? "http://localhost:8080";

const config: NextConfig = {
  // Docker builds set STANDALONE=1 for a minimal runtime image.
  output: process.env.STANDALONE === "1" ? "standalone" : undefined,
  poweredByHeader: false,
  async rewrites() {
    return [{ source: "/api/v1/:path*", destination: `${apiURL}/api/v1/:path*` }];
  },
  async headers() {
    return [
      {
        source: "/:path*",
        headers: [
          { key: "X-Content-Type-Options", value: "nosniff" },
          { key: "X-Frame-Options", value: "DENY" },
          { key: "Referrer-Policy", value: "same-origin" },
        ],
      },
    ];
  },
};

export default config;
