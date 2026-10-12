import type { Metadata } from "next";
import { Suspense } from "react";
import { PageHeader, PageLoading } from "@/components/page-header";
import { Imports } from "@/components/imports";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("imports") };
export default function Page() {
  return <>
    <PageHeader page="imports" />
    {/* The selected import comes from the address, which is only known in the browser. */}
    <Suspense fallback={<PageLoading page="imports" />}>
      <Imports />
    </Suspense>
  </>;
}
