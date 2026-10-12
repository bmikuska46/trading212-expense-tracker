import type { Metadata } from "next";
import { PageHeader } from "@/components/page-header";
import { Categories } from "@/components/catalog";
import { pageTitle } from "@/i18n/metadata";

export const metadata: Metadata = { title: pageTitle("categories") };
export default function Page() {
  return <>
    <PageHeader page="categories" />
    <Categories />
  </>;
}
