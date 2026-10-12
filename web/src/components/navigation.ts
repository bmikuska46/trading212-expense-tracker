import { ArrowDownToLine, ChartLine, LayoutDashboard, List, MessageSquare, Repeat, Settings, Store, Tags, Wallet, type LucideIcon } from "lucide-react";

// Page names live in the app.pages messages under these keys.
export type Page = "overview" | "transactions" | "accounts" | "categories" | "payees" | "subscriptions" | "investing" | "chat" | "imports" | "settings";

export type Destination = { href: string; page: Page; icon: LucideIcon };

export const navigation: Destination[] = [
  { href: "/", page: "overview", icon: LayoutDashboard },
  { href: "/transactions/", page: "transactions", icon: List },
  { href: "/accounts/", page: "accounts", icon: Wallet },
  { href: "/categories/", page: "categories", icon: Tags },
  { href: "/payees/", page: "payees", icon: Store },
  { href: "/subscriptions/", page: "subscriptions", icon: Repeat },
  { href: "/investing/", page: "investing", icon: ChartLine },
  { href: "/chat/", page: "chat", icon: MessageSquare },
  { href: "/imports/", page: "imports", icon: ArrowDownToLine },
  { href: "/settings/", page: "settings", icon: Settings },
];

// The rail groups pages by what they are for.
export const navigationGroups = [
  { title: "ledger", hrefs: ["/", "/transactions/", "/accounts/"] },
  { title: "organize", hrefs: ["/categories/", "/payees/", "/subscriptions/"] },
  { title: "investing", hrefs: ["/investing/"] },
  { title: "assistant", hrefs: ["/chat/"] },
  { title: "data", hrefs: ["/imports/", "/settings/"] },
] as const;
