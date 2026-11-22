import { defineConfig, devices } from "@playwright/test";

// Drives the real stack: Go API (CARGODESK_BIN) on :8080 against
// DATABASE_URL, and the production Next.js build on :3000.
// Set E2E_EXTERNAL=1 to test an already running stack (e.g. docker compose).
const bin = process.env.CARGODESK_BIN ?? "../api/bin/cargodesk";
const external = process.env.E2E_EXTERNAL === "1";
const baseURL = process.env.E2E_BASE_URL ?? "http://localhost:3000";

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  globalSetup: "./e2e/global-setup.ts",
  use: {
    baseURL,
    timezoneId: "America/Argentina/Buenos_Aires",
    locale: "en-US",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  webServer: external ? undefined : [
    {
      command: `${bin} serve`,
      url: "http://localhost:8080/healthz",
      env: { DEMO_AUTH: "true", SIGNUP_ENABLED: "true", ADDR: ":8080", DATABASE_URL: process.env.DATABASE_URL ?? "" },
      reuseExistingServer: !process.env.CI,
      stdout: "ignore",
    },
    {
      command: "npm run start",
      url: "http://localhost:3000/login",
      reuseExistingServer: !process.env.CI,
    },
  ],
});
