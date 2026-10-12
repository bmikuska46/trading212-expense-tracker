import type { Metadata } from "next";
import { PageHeader } from "@/components/page-header";
import { Payees } from "@/components/catalog";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("payees") };
export default function Page() {
  return <>
    <PageHeader page="payees" />
    <Payees />
  </>;
}
