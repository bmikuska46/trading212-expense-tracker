import type { Metadata } from "next";
import { PageHeader } from "@/components/page-header";
import { Subscriptions } from "@/components/subscriptions";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("subscriptions") };
export default function Page() {
  return <>
    <PageHeader page="subscriptions" />
    <Subscriptions />
  </>;
}
