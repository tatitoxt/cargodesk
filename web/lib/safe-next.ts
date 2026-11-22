// safeNext keeps post-login redirects on this origin: it rejects absolute,
// protocol-relative and backslash URLs, then re-checks the resolved origin.
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith("/") || next.startsWith("//") || next.includes("\\")) return "/";
  try {
    const u = new URL(next, window.location.origin);
    return u.origin === window.location.origin ? u.pathname + u.search + u.hash : "/";
  } catch {
    return "/";
  }
}
