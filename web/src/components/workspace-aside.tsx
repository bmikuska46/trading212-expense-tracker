"use client";

import type { ReactNode } from "react";
import { useTranslations } from "next-intl";

// WorkspaceAside is the settings side column, named for screen readers.
export function WorkspaceAside({ className, children }: { className?: string; children: ReactNode }) {
  const t = useTranslations("pages");
  return <aside aria-label={t("workspace")} className={className}>{children}</aside>;
}
