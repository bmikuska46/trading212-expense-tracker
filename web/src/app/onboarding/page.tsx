import type { Metadata } from "next";
import { Onboarding } from "@/components/onboarding";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("onboarding") };
export default function Page() {
  return <Onboarding />;
}
