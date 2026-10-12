import type { Metadata } from "next";
import { Suspense } from "react";
import { Chat } from "@/components/chat";
import { PageHeader, PageLoading } from "@/components/page-header";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("chat") };
export default function ChatPage() {
  return <>
    <PageHeader page="chat" />
    {/* The open chat comes from the address, which is only known in the browser. */}
    <Suspense fallback={<PageLoading page="chat" />}>
      <Chat />
    </Suspense>
  </>;
}
