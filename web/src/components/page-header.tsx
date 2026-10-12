"use client";

import type { ReactNode } from "react";
import { useTranslations } from "next-intl";
import { useDocumentTitle } from "@/components/locale";
import type { Page } from "@/components/navigation";
import { cn } from "@/lib/utils";

// PageHeader names the page in the compact console style. The overview
// speaks for itself, so its heading is for screen readers only. The name and
// description come from the messages for the page.
export function PageHeader({ page, children }: { page: Page; children?: ReactNode }) {
  const app = useTranslations("app");
  const t = useTranslations("pages");
  const title = app(`pages.${page}`);
  useDocumentTitle(title);
  if (page === "overview") return <h1 className="sr-only">{title}</h1>;
  const description = t(`descriptions.${page}`);
  return <header className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
    <div className="flex min-w-0 flex-col gap-1">
      <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
      <p className={cn("max-w-2xl text-sm text-muted-foreground max-sm:text-xs")}>{description}</p>
    </div>
    {children && <div className="flex flex-wrap items-center gap-2">{children}</div>}
  </header>;
}

// PageLoading stands in for a page while it reads the address.
export function PageLoading({ page }: { page: "overview" | "investing" | "chat" | "imports" | "transactions" }) {
  const t = useTranslations("pages.loading");
  return <p role="status" className="text-sm text-muted-foreground">{t(page)}</p>;
}
