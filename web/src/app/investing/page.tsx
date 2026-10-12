import type { Metadata } from "next";
import { Suspense } from "react";
import { Investing } from "@/components/investing";
import { InvestmentIncome } from "@/components/investment-income";
import { PageHeader, PageLoading } from "@/components/page-header";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("investing") };
export default function InvestingPage() {
  return <>
    <PageHeader page="investing" />
    {/* The range comes from the address, which is only known in the browser. */}
    <Suspense fallback={<PageLoading page="investing" />}>
      <Investing />
    </Suspense>
    <InvestmentIncome />
  </>;
}
