import type { Metadata } from "next";
import { PageHeader } from "@/components/page-header";
import { AccountSettings, Integrations, Users } from "@/components/owner-settings";
import { RateStatus } from "@/components/rate-status";
import { RuntimeStatus } from "@/components/runtime-status";
import { TaxSettings } from "@/components/tax-settings";
import { WorkspaceAside } from "@/components/workspace-aside";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("settings") };
export default function Settings() {
  return <>
    <PageHeader page="settings" />
    {/* The short account and status panels come first so they lead on phones;
        on wide screens they sit in a side column next to the integrations. */}
    <div className="grid items-start gap-3 xl:grid-cols-[minmax(0,1fr)_22rem]">
      <WorkspaceAside className="flex flex-col gap-3 xl:col-start-2 xl:row-start-1">
        <AccountSettings />
        <TaxSettings />
        <RateStatus />
        <RuntimeStatus />
      </WorkspaceAside>
      <div className="flex min-w-0 flex-col gap-3">
        <Users />
        <Integrations />
      </div>
    </div>
  </>;
}
