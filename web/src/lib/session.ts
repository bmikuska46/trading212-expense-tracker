"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { getSession, type Session } from "@/lib/api";

export function useSession() {
  return useQuery({ queryKey: ["session"], queryFn: getSession, staleTime: 60_000 });
}

// useSetSession replaces the cached session after setup, sign-in, or sign-out
// and drops cached private data when the owner changes state.
export function useSetSession() {
  const client = useQueryClient();
  return (session: Session) => {
    client.removeQueries({ predicate: (query) => query.queryKey[0] !== "session" && query.queryKey[0] !== "runtime" });
    client.setQueryData(["session"], session);
  };
}
