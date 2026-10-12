import type { Metadata } from "next";
import { PageHeader } from "@/components/page-header";
import { Accounts } from "@/components/accounts";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("accounts") };
export default function Page() {
  return <>
    <PageHeader page="accounts" />
    <Accounts />
  </>;
}
