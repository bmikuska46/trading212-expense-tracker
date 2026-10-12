"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import { useEffect, useState, type CSSProperties, type ReactNode } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useTranslations } from "next-intl";
import { useHotkey } from "@tanstack/react-hotkeys";
import { ChevronsUpDown, Keyboard, Languages, LayoutDashboard, List, LogOut, Menu, Plus, Search, Settings, ArrowDownToLine, SunMoon } from "lucide-react";
import { getScheduledImports, listNotifications, safeNext, signOut, type Session } from "@/lib/api";
import { dateFormat } from "@/lib/format";
import { useSession, useSetSession } from "@/lib/session";
import { CommandMenu } from "@/components/command-menu";
import { LocaleMenu, LocaleOptions } from "@/components/locale";
import { destinations, dialogOpen, KeyboardShortcuts, useModKey } from "@/components/keyboard-shortcuts";
import { navigation, navigationGroups } from "@/components/navigation";
import { Notifications } from "@/components/notifications";
import { ToolbarSlot } from "@/components/page-toolbar";
import { ThemeMenu, ThemeOptions } from "@/components/theme";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator, DropdownMenuSub,
  DropdownMenuSubContent, DropdownMenuSubTrigger, DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Kbd, KbdGroup } from "@/components/ui/kbd";
import {
  Sidebar, SidebarContent, SidebarFooter, SidebarGroup, SidebarGroupContent, SidebarGroupLabel, SidebarInset,
  SidebarMenu, SidebarMenuBadge, SidebarMenuButton, SidebarMenuItem, SidebarProvider, useSidebar,
} from "@/components/ui/sidebar";
import { cn } from "@/lib/utils";

// Bare pages are shown without the workspace around them.
const bareRoutes = new Set(["/login/", "/onboarding/", "/change-password/"]);

// redirectFor decides where the user belongs. The server enforces access on
// every financial endpoint; this only keeps the interface consistent with it.
// A user signed in with a temporary password must replace it first.
function redirectFor(current: string, session: Session | undefined): string | null {
  if (!session) return null;
  if (session.setupRequired) return current === "/onboarding/" ? null : "/onboarding/";
  if (!session.authenticated) {
    if (current === "/login/") return null;
    const next = current === "/" && !window.location.search ? "" : `?next=${encodeURIComponent(window.location.pathname + window.location.search)}`;
    return `/login/${next}`;
  }
  if (session.user?.mustChangePassword) return current === "/change-password/" ? null : "/change-password/";
  if (current === "/change-password/") return "/";
  return current === "/login/" ? safeNext(new URLSearchParams(window.location.search).get("next")) : null;
}

function Brand({ className }: { className?: string }) {
  const t = useTranslations("shell");
  return <Link href="/" className={cn("flex items-center gap-2.5 rounded-md font-mono text-sm font-semibold tracking-tight", className)}>
    <span aria-hidden="true" className="grid size-[18px] place-items-center rounded-[5px] bg-primary text-xs text-primary-foreground">€</span>
    {t("brand")}
  </Link>;
}

// useReviewCount counts imports waiting for review, shown as a badge on the
// Imports link. It shares the query that the alerts above each page use.
function useReviewCount(): number {
  const query = useQuery({ queryKey: ["notifications"], queryFn: listNotifications, refetchInterval: 60_000 });
  return query.data?.filter((item) => item.kind === "import_review").length ?? 0;
}

const shortDate = dateFormat({ day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

// StatusBar runs along the bottom edge with the next scheduled import and
// the keyboard hints.
function StatusBar({ onShortcuts }: { onShortcuts: () => void }) {
  const t = useTranslations("shell");
  const mod = useModKey();
  const schedule = useQuery({ queryKey: ["status", "scheduled-imports"], queryFn: getScheduledImports });
  return <footer className="sticky bottom-0 z-20 hidden h-(--status-height) shrink-0 items-center gap-6 border-t bg-background px-4 font-mono text-[0.6875rem] text-faint md:flex">
    {schedule.data?.enabled && <span className="max-xl:hidden">{t("nextImport", { date: shortDate.format(new Date(schedule.data.nextRunAt)) })}</span>}
    <span className="ml-auto flex items-center gap-4">
      <button type="button" onClick={onShortcuts} className="flex items-center gap-1.5 rounded-sm hover:text-foreground">
        <Kbd>?</Kbd>{t("shortcuts")}
      </button>
      <span className="flex items-center gap-1.5 max-lg:hidden"><KbdGroup><Kbd>{mod}</Kbd><Kbd>K</Kbd></KbdGroup>{t("commands")}</span>
      <span className="flex items-center gap-1.5 max-lg:hidden"><KbdGroup><Kbd>G</Kbd><Kbd>T</Kbd></KbdGroup>{t("goTo")}</span>
    </span>
  </footer>;
}

function AppSidebar({ current, email, reviewCount, onShortcuts, onSignOut, signingOut }: {
  current: string; email: string; reviewCount: number;
  onShortcuts: () => void; onSignOut: () => void; signingOut: boolean;
}) {
  const t = useTranslations("shell");
  const app = useTranslations("app");
  const { isMobile, setOpenMobile } = useSidebar();
  const close = () => { if (isMobile) setOpenMobile(false); };
  return <Sidebar collapsible="icon"
    className="md:top-(--header-height)! md:bottom-(--status-height)! md:h-auto!">
    <SidebarContent className="gap-2 pt-2">
      {isMobile && <div className="px-4 pt-3 pb-1"><Brand /></div>}
      {navigationGroups.map((group) => <SidebarGroup key={group.title}>
        <SidebarGroupLabel className="font-mono text-[0.625rem] tracking-[0.12em] text-faint uppercase">{app(`groups.${group.title}`)}</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu className="gap-px">
            {group.hrefs.map((href) => {
              const item = navigation.find((entry) => entry.href === href)!;
              const Icon = item.icon;
              const label = app(`pages.${item.page}`);
              const key = destinations.find((entry) => entry.href === href)?.key;
              const active = current === href;
              const badge = href === "/imports/" && reviewCount > 0;
              return <SidebarMenuItem key={href}>
                <SidebarMenuButton asChild isActive={active} tooltip={label}
                  className="text-muted-foreground data-active:text-foreground data-active:shadow-[inset_2px_0_0_var(--color-mark)]">
                  <Link href={href} aria-current={active ? "page" : undefined} onClick={close}>
                    <Icon aria-hidden="true" /><span>{label}</span>
                  </Link>
                </SidebarMenuButton>
                {badge ? <SidebarMenuBadge className="bg-spending font-mono text-[0.625rem] font-semibold text-background peer-data-active/menu-button:text-background">
                  {reviewCount}<span className="sr-only"> {t("importsNeedReview", { count: reviewCount })}</span>
                </SidebarMenuBadge> : key && !isMobile && <SidebarMenuBadge aria-hidden="true" className="font-mono text-[0.625rem] tracking-[0.08em] text-faint">
                  G {key}
                </SidebarMenuBadge>}
              </SidebarMenuItem>;
            })}
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>)}
    </SidebarContent>
    <SidebarFooter className="border-t">
      <SidebarMenu>
        <SidebarMenuItem>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <SidebarMenuButton size="lg" tooltip={email} className="text-muted-foreground data-[state=open]:bg-sidebar-accent">
                <span aria-hidden="true" className="grid size-7 shrink-0 place-items-center rounded-md border border-input bg-muted text-xs font-medium text-foreground uppercase">
                  {email.slice(0, 1)}
                </span>
                <span className="min-w-0 flex-1 truncate text-xs">{email}</span>
                <ChevronsUpDown className="ml-auto size-4" aria-hidden="true" />
              </SidebarMenuButton>
            </DropdownMenuTrigger>
            <DropdownMenuContent side={isMobile ? "top" : "right"} align="end" className="min-w-56">
              <DropdownMenuLabel className="truncate font-normal text-muted-foreground">{t("signedInAs", { email })}</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem asChild><Link href="/settings/" onClick={close}><Settings aria-hidden="true" />{app("pages.settings")}</Link></DropdownMenuItem>
              <DropdownMenuItem onSelect={() => { close(); onShortcuts(); }}><Keyboard aria-hidden="true" />{t("keyboardShortcuts")}</DropdownMenuItem>
              <DropdownMenuSub>
                <DropdownMenuSubTrigger><SunMoon aria-hidden="true" />{app("theme.label")}</DropdownMenuSubTrigger>
                <DropdownMenuSubContent><ThemeOptions /></DropdownMenuSubContent>
              </DropdownMenuSub>
              <DropdownMenuSub>
                <DropdownMenuSubTrigger><Languages aria-hidden="true" />{app("language")}</DropdownMenuSubTrigger>
                <DropdownMenuSubContent><LocaleOptions /></DropdownMenuSubContent>
              </DropdownMenuSub>
              <DropdownMenuSeparator />
              <DropdownMenuItem disabled={signingOut} onSelect={onSignOut}>
                <LogOut aria-hidden="true" />{signingOut ? t("signingOut") : t("signOut")}
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </SidebarMenuItem>
      </SidebarMenu>
    </SidebarFooter>
  </Sidebar>;
}

function TopBar({ onSearch, onNew, setSlot, reviewCount }: {
  onSearch: () => void; onNew: () => void; setSlot: (node: HTMLElement | null) => void; reviewCount: number;
}) {
  const t = useTranslations("shell");
  const mod = useModKey();
  return <header className="sticky top-0 z-30 flex h-(--header-height) shrink-0 items-center gap-3 border-b bg-background/90 px-3 backdrop-blur md:px-0">
    <div className="flex items-center md:w-(--sidebar-width) md:px-4"><Brand /></div>
    <button type="button" onClick={onSearch} aria-keyshortcuts="Meta+K Control+K"
      className="hidden h-8 max-w-xl flex-1 items-center gap-2.5 rounded-lg border border-input bg-card px-2.5 text-left text-sm text-faint transition-colors hover:border-muted-foreground/40 hover:text-muted-foreground md:flex">
      <Search className="size-4 shrink-0" aria-hidden="true" />
      <span className="flex-1 truncate">{t("search")}</span>
      <KbdGroup><Kbd>{mod}</Kbd><Kbd>K</Kbd></KbdGroup>
    </button>
    <div className="ml-auto flex items-center gap-2 md:pr-4">
      <div ref={setSlot} className="hidden items-center gap-2 md:flex" />
      <ThemeMenu />
      <Button onClick={onNew} aria-keyshortcuts="N" className="hidden md:inline-flex">
        <Plus data-icon="inline-start" aria-hidden="true" />{t("new")}
        <Kbd className="border-primary-foreground/20 bg-primary-foreground/10 text-primary-foreground">N</Kbd>
      </Button>
      <Button variant="outline" size="icon" className="md:hidden" onClick={onSearch} aria-label={t("searchAndCommands")}>
        <Search aria-hidden="true" />
      </Button>
      <Button variant="outline" size="icon" className="relative md:hidden" asChild>
        <Link href="/imports/" aria-label={t("importsWaiting", { count: reviewCount })}>
          <ArrowDownToLine aria-hidden="true" />
          {reviewCount > 0 && <span aria-hidden="true" className="absolute top-1 right-1 size-2 rounded-full bg-spending ring-2 ring-background" />}
        </Link>
      </Button>
    </div>
  </header>;
}

// MobileNav is the floating tab bar on phones. More opens the full
// navigation as a sheet.
function MobileNav({ current, onNew, reviewCount }: { current: string; onNew: () => void; reviewCount: number }) {
  const t = useTranslations("shell");
  const app = useTranslations("app");
  const { setOpenMobile } = useSidebar();
  const tab = "flex flex-col items-center gap-0.5 rounded-xl py-1.5 text-[0.6875rem] text-faint aria-[current=page]:text-foreground [&_svg]:size-5 aria-[current=page]:[&_svg]:text-mark";
  const tabs = [
    { href: "/", label: app("pages.overview"), icon: LayoutDashboard },
    { href: "/transactions/", label: t("register"), icon: List },
  ];
  return <nav aria-label={t("quickNavigation")} className="fixed inset-x-2.5 bottom-[calc(0.625rem+env(safe-area-inset-bottom))] z-30 grid grid-cols-5 rounded-2xl border border-input bg-popover/95 p-1.5 backdrop-blur-md md:hidden">
    {tabs.map(({ href, label, icon: Icon }) => <Link key={href} href={href} aria-current={current === href ? "page" : undefined} className={tab}>
      <Icon aria-hidden="true" />{label}
    </Link>)}
    <button type="button" onClick={onNew} aria-label={t("recordActivity")} className="mx-2 my-0.5 grid place-items-center rounded-xl bg-primary text-primary-foreground [&_svg]:size-6">
      <Plus aria-hidden="true" />
    </button>
    <Link href="/imports/" aria-current={current === "/imports/" ? "page" : undefined} className={cn(tab, "relative")}>
      <ArrowDownToLine aria-hidden="true" />{app("pages.imports")}
      {reviewCount > 0 && <span className="absolute top-1 right-[calc(50%-1.1rem)] rounded-sm bg-spending px-1 font-mono text-[0.625rem] leading-4 font-semibold text-background">
        {reviewCount}<span className="sr-only"> {t("waitingForReview")}</span>
      </span>}
    </Link>
    <button type="button" onClick={() => setOpenMobile(true)} className={tab}>
      <Menu aria-hidden="true" />{t("more")}
    </button>
  </nav>;
}

function Workspace({ current, email, children }: { current: string; email: string; children: ReactNode }) {
  const t = useTranslations("shell");
  const router = useRouter();
  const setSession = useSetSession();
  const reviewCount = useReviewCount();
  const [notice, setNotice] = useState({ text: "", error: false });
  const [commandOpen, setCommandOpen] = useState(false);
  const [shortcutsOpen, setShortcutsOpen] = useState(false);
  const [slot, setSlot] = useState<HTMLElement | null>(null);
  const leave = useMutation({
    mutationFn: signOut,
    onSuccess: () => setSession({ setupRequired: false, authenticated: false, user: null }),
  });
  // The register owns the transaction editor, so recording activity from
  // anywhere opens it there. On the register itself the filters are kept.
  const create = () => {
    // Each request carries a fresh value so the register tells it apart
    // from one it already handled.
    const stamp = String(Date.now());
    if (current !== "/transactions/") return router.push(`/transactions/?new=${stamp}`);
    const params = new URLSearchParams(window.location.search);
    params.set("new", stamp);
    router.replace(`/transactions/?${params}`, { scroll: false });
  };
  // The register handles N itself so the editor opens without a navigation.
  useHotkey("N", () => { if (!dialogOpen()) create(); }, { enabled: current !== "/transactions/", conflictBehavior: "allow" });

  return <SidebarProvider className="flex-col" style={{ "--sidebar-width": "13.75rem" } as CSSProperties}>
    <a href="#main" className="sr-only focus:not-sr-only focus:fixed focus:top-3 focus:left-3 focus:z-50 focus:rounded-md focus:bg-background focus:p-3">{t("skipToContent")}</a>
    <TopBar onSearch={() => setCommandOpen(true)} onNew={create} setSlot={setSlot} reviewCount={reviewCount} />
    <div className="flex flex-1">
      <AppSidebar current={current} email={email} reviewCount={reviewCount}
        onShortcuts={() => setShortcutsOpen(true)} onSignOut={() => leave.mutate()} signingOut={leave.isPending} />
      <SidebarInset id="main" tabIndex={-1} className="min-w-0 outline-none">
        <ToolbarSlot value={slot}>
          <div className="mx-auto flex w-full max-w-[92rem] flex-col gap-4 px-3.5 pt-3 pb-28 md:px-5 md:pt-5 md:pb-8">
            {leave.isError && <p role="alert" className="text-sm text-destructive">{leave.error.message}</p>}
            <p aria-live="polite" role={notice.error ? "alert" : undefined}
              className={cn("text-sm empty:hidden", notice.error ? "text-destructive" : "text-muted-foreground")}>{notice.text}</p>
            <Notifications report={(text, error) => setNotice({ text, error })} />
            {children}
          </div>
        </ToolbarSlot>
      </SidebarInset>
    </div>
    <StatusBar onShortcuts={() => setShortcutsOpen(true)} />
    <MobileNav current={current} onNew={create} reviewCount={reviewCount} />
    <CommandMenu open={commandOpen} onOpenChange={setCommandOpen} onNew={create}
      onShortcuts={() => setShortcutsOpen(true)} onSignOut={() => leave.mutate()} />
    <KeyboardShortcuts open={shortcutsOpen} onOpenChange={setShortcutsOpen} />
  </SidebarProvider>;
}

export function AppShell({ children }: { children: ReactNode }) {
  const t = useTranslations("shell");
  const pathname = usePathname();
  const router = useRouter();
  const current = pathname === "/" ? "/" : `${pathname.replace(/\/$/, "")}/`;
  const session = useSession();
  const target = redirectFor(current, session.data);
  useEffect(() => {
    if (target) router.replace(target);
  }, [router, target]);

  if (session.isError && !session.data) {
    return <main id="main" className="mx-auto flex min-h-svh max-w-md flex-col justify-center gap-4 px-6">
      <Brand className="w-fit" />
      <p role="alert" className="text-destructive">{session.error.message}</p>
      <Button className="w-fit" onClick={() => { void session.refetch(); }}>{t("tryAgain")}</Button>
    </main>;
  }
  if (!session.data || target) {
    return <main id="main" className="flex min-h-svh items-center justify-center" aria-busy="true">
      <p className="font-mono text-xs text-muted-foreground" role="status">{t("loadingWorkspace")}</p>
    </main>;
  }
  if (bareRoutes.has(current)) {
    return <div className="flex min-h-svh flex-col">
      <header className="flex h-(--header-height) items-center justify-between border-b px-4 md:px-6">
        <Brand className="w-fit" /><span className="flex items-center gap-2"><LocaleMenu /><ThemeMenu /></span>
      </header>
      <main id="main" tabIndex={-1} className="mx-auto flex w-full max-w-xl flex-1 flex-col gap-6 px-4 py-10 outline-none md:py-16">{children}</main>
    </div>;
  }
  return <Workspace current={current} email={session.data.user?.email ?? ""}>{children}</Workspace>;
}
