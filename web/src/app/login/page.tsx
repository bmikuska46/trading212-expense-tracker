import type { Metadata } from "next";
import { SignInForm } from "@/components/sign-in-form";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("login") };
export default function Page() {
  return <SignInForm />;
}
