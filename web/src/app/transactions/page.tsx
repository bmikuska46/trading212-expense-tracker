import type { Metadata } from "next";
import { Suspense } from "react";
import { PageHeader, PageLoading } from "@/components/page-header";
import { Transactions } from "@/components/transactions";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("transactions") };
export default function Page() {
  return <>
    <PageHeader page="transactions" />
    {/* The selected account comes from the address, which is only known in the browser. */}
    <Suspense fallback={<PageLoading page="transactions" />}>
      <Transactions />
    </Suspense>
  </>;
}
