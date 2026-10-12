"use client";

import { useRouter } from "next/navigation";
import { useSyncExternalStore } from "react";
import { useHotkey, useHotkeySequences } from "@tanstack/react-hotkeys";
import { useTranslations } from "next-intl";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Kbd, KbdGroup } from "@/components/ui/kbd";

// dialogOpen reports a modal on screen. Page shortcuts stay quiet while one
// is open, so keys reach the dialog instead of the page behind it.
export function dialogOpen(): boolean {
  return document.querySelector('[role="dialog"], [role="alertdialog"]') !== null;
}

// useModKey names the command modifier: the Command key on Apple devices and
// Ctrl elsewhere.
export function useModKey(): string {
  return useSyncExternalStore(() => () => {}, () => /Mac|iPhone|iPad/.test(navigator.userAgent) ? "⌘" : "Ctrl", () => "Ctrl");
}

export const destinations = [
  { key: "D", href: "/", page: "overview" },
  { key: "A", href: "/accounts/", page: "accounts" },
  { key: "T", href: "/transactions/", page: "transactions" },
  { key: "C", href: "/categories/", page: "categories" },
  { key: "P", href: "/payees/", page: "payees" },
  { key: "U", href: "/subscriptions/", page: "subscriptions" },
  { key: "V", href: "/investing/", page: "investing" },
  { key: "Q", href: "/chat/", page: "chat" },
  { key: "I", href: "/imports/", page: "imports" },
  { key: "S", href: "/settings/", page: "settings" },
] as const;

// Group titles and labels are keys in the shortcuts messages.
type Item = "showShortcuts" | "commands" | "record" | "sidebar" | "refreshStatus" | "previousMonth" | "nextMonth" | "search"
  | "nextRow" | "previousRow" | "toggleRow" | "openRow" | "previousPage" | "nextPage" | "clearSelection";

const groups: { title: "anywhere" | "overview" | "transactions"; items: { keys: string[]; label: Item }[] }[] = [
  {
    title: "anywhere",
    items: [
      { keys: ["?"], label: "showShortcuts" },
      { keys: ["Mod+K"], label: "commands" },
      { keys: ["N"], label: "record" },
      { keys: ["Mod+B"], label: "sidebar" },
      { keys: ["Shift+R"], label: "refreshStatus" },
    ],
  },
  {
    title: "overview",
    items: [{ keys: ["["], label: "previousMonth" }, { keys: ["]"], label: "nextMonth" }],
  },
  {
    title: "transactions",
    items: [
      { keys: ["/"], label: "search" },
      { keys: ["J"], label: "nextRow" },
      { keys: ["K"], label: "previousRow" },
      { keys: ["X"], label: "toggleRow" },
      { keys: ["O"], label: "openRow" },
      { keys: ["["], label: "previousPage" },
      { keys: ["]"], label: "nextPage" },
      { keys: ["Esc"], label: "clearSelection" },
    ],
  },
];

function Keys({ keys }: { keys: string[] }) {
  const t = useTranslations("shortcuts");
  const mod = useModKey();
  return <span className="flex shrink-0 items-center gap-1">
    {keys.map((key, index) => <span key={index} className="flex items-center gap-1">
      {index > 0 && <span className="text-xs text-muted-foreground">{t("then")}</span>}
      <KbdGroup>{key.split(/\+(?=.)/).map((part) => <Kbd key={part}>{part === "Mod" ? mod : part}</Kbd>)}</KbdGroup>
    </span>)}
  </span>;
}

// KeyboardShortcuts registers the shortcuts that work on every page and
// lists all of them. Single-key shortcuts never fire while typing in a field.
export function KeyboardShortcuts({ open, onOpenChange: setOpen }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const t = useTranslations("shortcuts");
  const app = useTranslations("app");
  const router = useRouter();
  useHotkeySequences(destinations.map(({ key, href }) => ({
    sequence: ["G", key],
    callback: () => { if (!dialogOpen()) router.push(href); },
  })), { ignoreInputs: true });
  useHotkey("?", () => { if (!dialogOpen()) setOpen(true); }, { enabled: !open });
  return <>
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-h-[calc(100vh-2rem)] overflow-y-auto sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("title")}</DialogTitle>
          <DialogDescription>{t("description")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-5">
          {groups.map((group) => {
            // Going to each page works from anywhere, after the general shortcuts.
            const items = [
              ...group.items.map((item) => ({ keys: item.keys, label: t(`items.${item.label}`) })),
              ...group.title === "anywhere" ? destinations.map(({ key, page }) => ({ keys: ["G", key], label: t("goTo", { page: app(`pages.${page}`) }) })) : [],
            ];
            return <section key={group.title} aria-label={t(`groups.${group.title}`)}>
              <h3 className="mb-2 font-medium">{t(`groups.${group.title}`)}</h3>
              <dl className="flex flex-col gap-1.5">
                {items.map((item) => <div key={item.label} className="flex items-center justify-between gap-4">
                  <dt className="text-muted-foreground">{item.label}</dt>
                  <dd><Keys keys={item.keys} /></dd>
                </div>)}
              </dl>
            </section>;
          })}
        </div>
      </DialogContent>
    </Dialog>
  </>;
}
