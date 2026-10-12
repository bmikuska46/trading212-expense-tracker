import { Suspense } from "react";
import { Dashboard } from "@/components/dashboard";
import { PageHeader, PageLoading } from "@/components/page-header";

export default function Overview() {
  return <>
    <PageHeader page="overview" />
    {/* The month and account come from the address, which is only known in the browser. */}
    <Suspense fallback={<PageLoading page="overview" />}>
      <Dashboard />
    </Suspense>
  </>;
}
