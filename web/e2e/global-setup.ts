import { execFileSync } from "node:child_process";

// Reload the synthetic seed so every run starts from the same data.
export default function globalSetup() {
  if (process.env.E2E_SKIP_SEED === "1") return;
  const bin = process.env.CARGODESK_BIN ?? "../api/bin/cargodesk";
  execFileSync(bin, ["seed", "-reset"], { stdio: "inherit", env: process.env });
}
