"use client";

import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { usePathname, useRouter } from "next/navigation";
import { api, ApiError, type Me } from "./api";

const SessionContext = createContext<Me | null>(null);

export function useMe(): Me {
  const me = useContext(SessionContext);
  if (!me) throw new Error("useMe outside RequireSession");
  return me;
}

export function RequireSession({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null);
  const router = useRouter();
  const pathname = usePathname();
  useEffect(() => {
    api<Me>("/me")
      .then((m) => {
        if (m.must_change_password && pathname !== "/password") {
          router.replace(`/password?next=${encodeURIComponent(pathname)}`);
          return;
        }
        setMe(m);
      })
      .catch((e) => {
        if (e instanceof ApiError && e.status === 401) router.replace(`/login?next=${encodeURIComponent(pathname)}`);
      });
  }, [router, pathname]);
  if (!me) return <p className="muted">Loading…</p>;
  return <SessionContext.Provider value={me}>{children}</SessionContext.Provider>;
}
