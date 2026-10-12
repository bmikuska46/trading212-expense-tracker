"use client";

import { useTranslations } from "next-intl";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Badge } from "@/components/ui/badge";

export function PlannedPage({ title, description, detail }: { title: string; description: string; detail: string }) {
  const t = useTranslations("pages.planned");
  return <>
    <div className="flex flex-col gap-3">
      <Badge variant="outline">{t("soon")}</Badge>
      <h1 className="text-3xl font-semibold tracking-tight">{title}</h1>
      <p className="max-w-xl text-muted-foreground">{description}</p>
    </div>
    <Empty className="min-h-64 border">
      <EmptyHeader>
        <EmptyTitle>{t("title")}</EmptyTitle>
        <EmptyDescription>{detail}</EmptyDescription>
      </EmptyHeader>
    </Empty>
  </>;
}
