"use client";

import Link from "next/link";
import { useTranslations } from "next-intl";
import { Button } from "@/components/ui/button";
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from "@/components/ui/empty";

export default function NotFound() {
  const t = useTranslations("pages.notFound");
  return <Empty className="min-h-[60vh]">
    <EmptyHeader>
      <p className="font-mono text-xs tracking-wider text-faint uppercase">{t("code")}</p>
      <h1 className="text-xl font-medium tracking-tight">{t("title")}</h1>
      <EmptyDescription>{t("description")}</EmptyDescription>
    </EmptyHeader>
    <EmptyContent>
      <Button asChild size="sm"><Link href="/">{t("back")}</Link></Button>
    </EmptyContent>
  </Empty>;
}
