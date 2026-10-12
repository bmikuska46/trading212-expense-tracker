import type { Metadata } from "next";
import { ChangePasswordPage } from "@/components/password-form";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("changePassword") };
export default function Page() {
  return <ChangePasswordPage />;
}
