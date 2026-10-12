"use client";

import { useRouter } from "next/navigation";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useHotkey } from "@tanstack/react-hotkeys";
import { useTranslations } from "next-intl";
import { ArrowDownToLine, Keyboard, LogOut, Plus, Search, Tags, Wallet } from "lucide-react";
import { listAccounts, listCategories } from "@/lib/api";
import { destinations } from "@/components/keyboard-shortcuts";
import { setTheme, themeIcons } from "@/components/theme";
import { themes } from "@/lib/theme";
import { navigation } from "@/components/navigation";
import {
  CommandDialog, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList, CommandSeparator, CommandShortcut,
} from "@/components/ui/command";

// CommandMenu searches the ledger and runs any action by name. It opens with
// Mod+K from anywhere, including while typing in a field.
export function CommandMenu({ open, onOpenChange, onNew, onShortcuts, onSignOut }: {
  open: boolean; onOpenChange: (open: boolean) => void;
  onNew: () => void; onShortcuts: () => void; onSignOut: () => void;
}) {
  const t = useTranslations("commands");
  const app = useTranslations("app");
  const shell = useTranslations("shell");
  const router = useRouter();
  const [search, setSearch] = useState("");
  useHotkey("Mod+K", () => onOpenChange(!open), { ignoreInputs: false });
  const accounts = useQuery({ queryKey: ["accounts"], queryFn: listAccounts, enabled: open });
  const categories = useQuery({ queryKey: ["categories"], queryFn: listCategories, enabled: open });
  const run = (action: () => void) => {
    onOpenChange(false);
    setSearch("");
    action();
  };
  const term = search.trim();
  return <CommandDialog open={open} onOpenChange={(next) => { onOpenChange(next); if (!next) setSearch(""); }}
    title={t("title")} description={t("description")}>
    <CommandInput placeholder={shell("search")} value={search} onValueChange={setSearch} maxLength={100} />
    <CommandList>
      <CommandEmpty>{t("empty")}</CommandEmpty>
      {term && <CommandGroup heading={t("search")}>
        <CommandItem value={`search ${term}`} onSelect={() => run(() => router.push(`/transactions/?q=${encodeURIComponent(term)}`))}>
          <Search aria-hidden="true" />{t("searchFor", { term })}
        </CommandItem>
      </CommandGroup>}
      <CommandGroup heading={t("actions")}>
        <CommandItem onSelect={() => run(onNew)}><Plus aria-hidden="true" />{shell("recordActivity")}<CommandShortcut>N</CommandShortcut></CommandItem>
        <CommandItem onSelect={() => run(() => router.push("/imports/"))}><ArrowDownToLine aria-hidden="true" />{t("importCsv")}</CommandItem>
        <CommandItem onSelect={() => run(onShortcuts)}><Keyboard aria-hidden="true" />{shell("keyboardShortcuts")}<CommandShortcut>?</CommandShortcut></CommandItem>
        <CommandItem onSelect={() => run(onSignOut)}><LogOut aria-hidden="true" />{shell("signOut")}</CommandItem>
      </CommandGroup>
      <CommandSeparator />
      <CommandGroup heading={app("theme.label")}>
        {themes.map((value) => {
          const Icon = themeIcons[value];
          const label = t(value === "light" ? "themeLight" : value === "dark" ? "themeDark" : "themeSystem");
          return <CommandItem key={value} value={`theme ${label}`} onSelect={() => run(() => setTheme(value))}>
            <Icon aria-hidden="true" />{label}
          </CommandItem>;
        })}
      </CommandGroup>
      <CommandSeparator />
      <CommandGroup heading={t("goTo")}>
        {navigation.map(({ href, page, icon: Icon }) => {
          const label = app(`pages.${page}`);
          const key = destinations.find((item) => item.href === href)?.key;
          return <CommandItem key={href} value={`go ${label}`} onSelect={() => run(() => router.push(href))}>
            <Icon aria-hidden="true" />{label}{key && <CommandShortcut>G {key}</CommandShortcut>}
          </CommandItem>;
        })}
      </CommandGroup>
      {accounts.data && accounts.data.length > 0 && <CommandGroup heading={app("pages.accounts")}>
        {accounts.data.filter((account) => !account.archived).map((account) => <CommandItem key={account.id} value={`account ${account.name}`}
          onSelect={() => run(() => router.push(`/transactions/?account=${account.id}`))}>
          <Wallet aria-hidden="true" />{account.name}<CommandShortcut>{account.currency}</CommandShortcut>
        </CommandItem>)}
      </CommandGroup>}
      {categories.data && categories.data.length > 0 && <CommandGroup heading={app("pages.categories")}>
        {categories.data.map((category) => <CommandItem key={category.id} value={`category ${category.name}`}
          onSelect={() => run(() => router.push(`/transactions/?category=${category.id}`))}>
          <Tags aria-hidden="true" />{category.name}
        </CommandItem>)}
      </CommandGroup>}
    </CommandList>
  </CommandDialog>;
}
