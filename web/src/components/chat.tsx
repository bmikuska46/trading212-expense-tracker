"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useRef, useState, type FormEvent, type KeyboardEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslations } from "next-intl";
import { ArrowRight, ArrowUp, History, MessageSquare, MoreHorizontal, Pencil, Plus, Trash2, TriangleAlert } from "lucide-react";
import {
  askInChat, createChat, deleteChat, getChat, listChats, listIntegrations, renameChat,
  type Chat as ChatSummary, type ChatAnswer, type ChatColumn, type ChatDetail, type ChatList, type ChatReply,
} from "@/lib/api";
import { dateFormat, formatMinor, numberFormat } from "@/lib/format";
import { translator } from "@/i18n/translate";
import { alertClass, alertDescriptionClass, cellClass, headClass } from "@/components/panel";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

const maxQuestion = 500;
const counts = numberFormat();
const percents = numberFormat({ style: "percent", maximumFractionDigits: 1 });
const days = dateFormat({ day: "numeric", month: "short", year: "numeric", timeZone: "UTC" });
const moments = dateFormat({ day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

const errorText = (error: unknown) => error instanceof Error ? error.message : translator()("common.errors.generic");

function chatIdOf(value: string | null): number | null {
  const id = Number(value);
  return Number.isInteger(id) && id > 0 ? id : null;
}

// Chat shows a new chat at /chat/ and an earlier one at /chat/?id=N.
export function Chat() {
  const t = useTranslations("chat");
  const params = useSearchParams();
  const router = useRouter();
  const client = useQueryClient();
  const chatId = chatIdOf(params.get("id"));
  const [historyOpen, setHistoryOpen] = useState(false);
  const [draft, setDraft] = useState("");
  const [pending, setPending] = useState<string | null>(null);
  const composer = useRef<HTMLTextAreaElement>(null);
  const end = useRef<HTMLDivElement>(null);

  const chats = useQuery({ queryKey: ["chats"], queryFn: listChats });
  const chat = useQuery({ queryKey: ["chat", chatId], queryFn: ({ signal }) => getChat(chatId!, { signal }), enabled: chatId !== null });
  const integrations = useQuery({ queryKey: ["integrations"], queryFn: listIntegrations });
  const ready = integrations.data?.some((item) => item.provider === "typesafe" && item.configured) ?? false;

  const ask = useMutation({
    mutationFn: (question: string) => chatId === null ? createChat(question) : askInChat(chatId, question),
    onMutate: (question) => setPending(question),
    onSuccess: ({ chat: summary, message }: ChatReply) => {
      client.setQueryData<ChatDetail>(["chat", summary.id], (current) =>
        ({ ...summary, messages: [...(current?.messages ?? []), message] }));
      client.setQueryData<ChatList>(["chats"], (current) => current &&
        ({ ...current, chats: [summary, ...current.chats.filter((item) => item.id !== summary.id)] }));
      if (chatId !== summary.id) router.replace(`/chat/?id=${summary.id}`, { scroll: false });
    },
    onError: (_error, question) => setDraft((current) => current || question),
    onSettled: () => setPending(null),
  });

  // A different chat starts with an empty composer and no old error.
  const { reset } = ask;
  useEffect(() => {
    reset();
    composer.current?.focus();
  }, [chatId, reset]);

  const messages = chatId === null ? [] : chat.data?.messages ?? [];
  useEffect(() => {
    end.current?.scrollIntoView({ block: "end", behavior: "smooth" });
  }, [messages.length, pending]);

  const send = (text: string) => {
    const question = text.trim();
    if (!question || question.length > maxQuestion || ask.isPending) return;
    setDraft("");
    ask.mutate(question);
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    send(draft);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
      event.preventDefault();
      send(draft);
    }
  };

  const history = <ChatHistory chats={chats.data?.chats} loading={chats.isPending} error={chats.error} current={chatId} />;
  const missing = chatId !== null && chat.isError;
  const empty = chatId === null || (chat.isSuccess && messages.length === 0);

  return <div className="grid gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]">
    <aside aria-label={t("history")} className="flex flex-col gap-2 max-lg:hidden">
      <Button asChild variant="outline" className="justify-start">
        <Link href="/chat/"><Plus data-icon="inline-start" aria-hidden="true" />{t("newChat")}</Link>
      </Button>
      {history}
    </aside>
    <Sheet open={historyOpen} onOpenChange={setHistoryOpen}>
      <SheetContent side="left" className="w-80 gap-0">
        <SheetHeader>
          <SheetTitle>{t("history")}</SheetTitle>
          <SheetDescription>{t("historyDescription")}</SheetDescription>
        </SheetHeader>
        {/* Opening a chat from the list closes the sheet. */}
        <div className="overflow-y-auto px-4 pb-4"
          onClick={(event) => { if ((event.target as HTMLElement).closest("a")) setHistoryOpen(false); }}>{history}</div>
      </SheetContent>
    </Sheet>

    <section aria-label={chat.data?.title ?? t("newChat")} className="flex min-w-0 flex-col gap-4">
      <div className="flex items-center gap-2 lg:hidden">
        <Button variant="outline" size="sm" onClick={() => setHistoryOpen(true)}>
          <History data-icon="inline-start" aria-hidden="true" />{t("historyButton")}
        </Button>
        <Button asChild variant="outline" size="sm">
          <Link href="/chat/"><Plus data-icon="inline-start" aria-hidden="true" />{t("newChat")}</Link>
        </Button>
      </div>

      {integrations.isSuccess && !ready && <Alert className={alertClass}>
        <TriangleAlert aria-hidden="true" />
        <AlertTitle>{t("notConnected.title")}</AlertTitle>
        <AlertDescription className={alertDescriptionClass}>
          <p>{t.rich("notConnected.body", { link: (chunks) => <Link href="/settings/" className="underline underline-offset-3">{chunks}</Link> })}</p>
        </AlertDescription>
      </Alert>}

      {missing && <Alert variant="destructive" className={alertClass}>
        <TriangleAlert aria-hidden="true" />
        <AlertTitle>{t("missing.title")}</AlertTitle>
        <AlertDescription className={alertDescriptionClass}>
          <p>{errorText(chat.error)} <Link href="/chat/" className="underline underline-offset-3">{t("missing.startNew")}</Link>.</p>
        </AlertDescription>
      </Alert>}

      {chatId !== null && chat.isPending && <div role="status" aria-label={t("loading")} className="flex flex-col gap-3">
        <Skeleton className="ml-auto h-9 w-1/2" /><Skeleton className="h-24 w-full" />
      </div>}

      {empty && !missing && !pending && <Welcome examples={chats.data?.examples ?? []} disabled={!ready || ask.isPending} onAsk={send} />}

      <ol className="flex flex-col gap-4" aria-live="polite">
        {messages.map((message) => <li key={message.id} className="flex flex-col gap-2">
          <Question text={message.question} />
          <AnswerCard answer={message.answer} disabled={!ready || ask.isPending} onAsk={send} />
        </li>)}
        {pending && <li className="flex flex-col gap-2">
          <Question text={pending} />
          <Card className="flex-row items-center gap-2 px-4 py-3 text-sm text-muted-foreground">
            <Spinner aria-hidden="true" /><span role="status">{t("reading")}</span>
          </Card>
        </li>}
      </ol>

      {ask.isError && <Alert variant="destructive" className={alertClass}>
        <TriangleAlert aria-hidden="true" />
        <AlertTitle>{t("notAnswered")}</AlertTitle>
        <AlertDescription className={alertDescriptionClass}><p>{errorText(ask.error)}</p></AlertDescription>
      </Alert>}
      <div ref={end} />

      <form noValidate onSubmit={submit}
        className="sticky bottom-[calc(5.5rem+env(safe-area-inset-bottom))] z-10 flex items-end gap-2 rounded-xl border border-input bg-background/95 p-2 backdrop-blur md:bottom-[calc(var(--status-height)+0.75rem)]">
        <label htmlFor="chat-question" className="sr-only">{t("question")}</label>
        <Textarea id="chat-question" ref={composer} value={draft} rows={1} maxLength={maxQuestion} disabled={!ready || missing}
          placeholder={ready ? t("placeholder") : t("placeholderOff")}
          className="max-h-40 min-h-9 resize-none border-0 bg-transparent shadow-none focus-visible:ring-0 dark:bg-transparent"
          aria-describedby="chat-hint" onChange={(event) => setDraft(event.target.value)} onKeyDown={onKeyDown} />
        <Button type="submit" size="icon" aria-label={t("ask")} disabled={!ready || missing || ask.isPending || !draft.trim()}>
          {ask.isPending ? <Spinner aria-hidden="true" /> : <ArrowUp aria-hidden="true" />}
        </Button>
      </form>
      <p id="chat-hint" className="-mt-2 text-xs text-faint">
        {t("hint")}{draft.length > maxQuestion - 100 && ` ${t("left", { count: maxQuestion - draft.length })}`}
      </p>
    </section>
  </div>;
}

function Question({ text }: { text: string }) {
  return <p className="ml-auto max-w-[85%] rounded-xl rounded-br-sm bg-muted px-3.5 py-2 text-sm whitespace-pre-wrap">{text}</p>;
}

function Welcome({ examples, disabled, onAsk }: { examples: ChatList["examples"]; disabled: boolean; onAsk: (question: string) => void }) {
  const t = useTranslations("chat.welcome");
  return <div className="flex flex-col gap-5 py-6">
    <div className="flex flex-col items-center gap-2 text-center">
      <span className="grid size-9 place-items-center rounded-lg bg-muted"><MessageSquare className="size-4" aria-hidden="true" /></span>
      <h2 className="text-base font-medium">{t("title")}</h2>
      <p className="max-w-md text-sm text-muted-foreground">
        {t("body")}
      </p>
    </div>
    <div className="grid gap-4 sm:grid-cols-2">
      {examples.map((group) => <div key={group.title} className="flex flex-col gap-1.5">
        <h3 className="font-mono text-[0.6875rem] font-medium tracking-wider text-faint uppercase">{group.title}</h3>
        <ul className="flex flex-col gap-1">
          {group.questions.map((question) => <li key={question}>
            <button type="button" disabled={disabled} onClick={() => onAsk(question)}
              className="w-full rounded-md border px-3 py-2 text-left text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:pointer-events-none disabled:opacity-50">
              {question}
            </button>
          </li>)}
        </ul>
      </div>)}
    </div>
  </div>;
}

function AnswerCard({ answer, disabled, onAsk }: { answer: ChatAnswer; disabled: boolean; onAsk: (question: string) => void }) {
  return <Card className="gap-3 px-4 py-3">
    <p className="text-sm leading-relaxed">{answer.text}</p>
    {answer.table && answer.table.rows.length > 0 && <AnswerTable columns={answer.table.columns} rows={answer.table.rows} />}
    {answer.notes && answer.notes.length > 0 && <ul className="flex flex-col gap-0.5 text-xs text-muted-foreground">
      {answer.notes.map((note) => <li key={note}>{note}</li>)}
    </ul>}
    {(answer.link || (answer.suggestions && answer.suggestions.length > 0)) && <div className="flex flex-wrap items-center gap-1.5">
      {answer.link && <Button asChild variant="outline" size="xs">
        <Link href={answer.link.href}>{answer.link.label}<ArrowRight data-icon="inline-end" aria-hidden="true" /></Link>
      </Button>}
      {answer.suggestions?.map((suggestion) => <Button key={suggestion} variant="ghost" size="xs" disabled={disabled}
        className="text-muted-foreground" onClick={() => onAsk(suggestion)}>{suggestion}</Button>)}
    </div>}
  </Card>;
}

type Cell = string | number | null;

function formatCell(value: Cell, column: ChatColumn): string {
  if (value === null || value === "") return "";
  if (typeof value === "string") return column.kind === "date" ? formatDay(value) : value;
  switch (column.kind) {
    case "money": return formatMinor(Math.round(value), column.currency ?? "EUR");
    case "percent": return percents.format(value);
    case "count": return counts.format(value);
    default: return String(value);
  }
}

function formatDay(value: string): string {
  const date = new Date(`${value}T00:00:00Z`);
  return Number.isNaN(date.getTime()) ? value : days.format(date);
}

function AnswerTable({ columns, rows }: { columns: ChatColumn[]; rows: Cell[][] }) {
  // Bars compare each value with the largest one in its column.
  const largest = columns.map((column, index) => column.bar
    ? Math.max(0, ...rows.map((row) => Math.abs(typeof row[index] === "number" ? row[index] : 0))) : 0);
  const numeric = (column: ChatColumn) => column.kind !== "text" && column.kind !== "date";
  return <div className="-mx-4 overflow-x-auto border-y">
    <Table>
      <TableHeader>
        <TableRow>
          {columns.map((column) => <TableHead key={column.label} className={cn(headClass, "pl-4 last:pr-4", numeric(column) && "text-right")}>
            {column.label}
          </TableHead>)}
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((row, rowIndex) => <TableRow key={rowIndex}>
          {columns.map((column, index) => {
            const value = row[index] ?? null;
            const width = column.bar && largest[index] > 0 && typeof value === "number" ? Math.abs(value) / largest[index] : 0;
            return <TableCell key={column.label} className={cn(cellClass, "pl-4 last:pr-4", numeric(column) && "text-right font-mono tabular-nums",
              typeof value === "number" && value < 0 && column.kind === "money" && "text-spending")}>
              {column.bar
                ? <span className="flex items-center justify-end gap-2">
                  <span aria-hidden="true" className="h-1.5 w-16 overflow-hidden rounded-full bg-muted max-sm:hidden">
                    <span className="block h-full rounded-full bg-mark" style={{ width: `${width * 100}%` }} />
                  </span>
                  {formatCell(value, column)}
                </span>
                : formatCell(value, column)}
            </TableCell>;
          })}
        </TableRow>)}
      </TableBody>
    </Table>
  </div>;
}

function ChatHistory({ chats, loading, error, current }: {
  chats: ChatSummary[] | undefined; loading: boolean; error: Error | null; current: number | null;
}) {
  const t = useTranslations("chat");
  const [renaming, setRenaming] = useState<ChatSummary | null>(null);
  const [deleting, setDeleting] = useState<ChatSummary | null>(null);
  if (loading) return <div role="status" aria-label={t("loadingChats")} className="flex flex-col gap-1.5">
    <Skeleton className="h-8 w-full" /><Skeleton className="h-8 w-full" /><Skeleton className="h-8 w-full" />
  </div>;
  if (error) return <p role="alert" className="text-xs text-destructive">{errorText(error)}</p>;
  if (!chats || chats.length === 0) return <p className="px-1 text-xs text-muted-foreground">{t("noChats")}</p>;
  return <>
    <ul className="flex flex-col gap-px">
      {chats.map((chat) => <li key={chat.id} className="group/chat relative">
        <Link href={`/chat/?id=${chat.id}`} aria-current={chat.id === current ? "page" : undefined}
          className="flex flex-col rounded-md py-1.5 pr-8 pl-2.5 text-sm text-muted-foreground hover:bg-muted hover:text-foreground aria-[current=page]:bg-muted aria-[current=page]:text-foreground">
          <span className="truncate">{chat.title}</span>
          <span className="font-mono text-[0.6875rem] text-faint">{moments.format(new Date(chat.updatedAt))}</span>
        </Link>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon-xs" aria-label={t("actionsFor", { title: chat.title })}
              className="absolute top-1.5 right-1 opacity-0 group-hover/chat:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100 max-lg:opacity-100">
              <MoreHorizontal aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onSelect={() => setRenaming(chat)}><Pencil aria-hidden="true" />{t("rename")}</DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onSelect={() => setDeleting(chat)}><Trash2 aria-hidden="true" />{t("delete")}</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </li>)}
    </ul>
    {renaming && <RenameDialog chat={renaming} onClose={() => setRenaming(null)} />}
    {deleting && <DeleteDialog chat={deleting} current={current} onClose={() => setDeleting(null)} />}
  </>;
}

function RenameDialog({ chat, onClose }: { chat: ChatSummary; onClose: () => void }) {
  const t = useTranslations("chat.renameDialog");
  const common = useTranslations("common");
  const client = useQueryClient();
  const [title, setTitle] = useState(chat.title);
  const rename = useMutation({
    mutationFn: () => renameChat(chat.id, title),
    onSuccess: (saved) => {
      client.setQueryData<ChatList>(["chats"], (current) => current &&
        ({ ...current, chats: current.chats.map((item) => item.id === saved.id ? saved : item) }));
      client.setQueryData<ChatDetail>(["chat", saved.id], (current) => current && { ...current, title: saved.title });
      onClose();
    },
  });
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (title.trim()) rename.mutate();
  };
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{t("title")}</DialogTitle>
        <DialogDescription>{t("description")}</DialogDescription>
      </DialogHeader>
      <form noValidate onSubmit={submit} className="flex flex-col gap-3">
        <label htmlFor="chat-title" className="sr-only">{t("label")}</label>
        <Input id="chat-title" value={title} maxLength={80} autoComplete="off" aria-invalid={rename.isError}
          onChange={(event) => setTitle(event.target.value)} />
        {rename.isError && <p role="alert" className="text-xs text-destructive">{errorText(rename.error)}</p>}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>{common("cancel")}</Button>
          <Button type="submit" disabled={rename.isPending || !title.trim()}>{rename.isPending ? common("saving") : t("save")}</Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>;
}

function DeleteDialog({ chat, current, onClose }: { chat: ChatSummary; current: number | null; onClose: () => void }) {
  const t = useTranslations("chat");
  const common = useTranslations("common");
  const client = useQueryClient();
  const router = useRouter();
  const remove = useMutation({
    mutationFn: () => deleteChat(chat.id),
    onSuccess: () => {
      client.setQueryData<ChatList>(["chats"], (list) => list && ({ ...list, chats: list.chats.filter((item) => item.id !== chat.id) }));
      client.removeQueries({ queryKey: ["chat", chat.id] });
      if (current === chat.id) router.replace("/chat/", { scroll: false });
      onClose();
    },
  });
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{t("deleteDialog.title")}</DialogTitle>
        <DialogDescription>{t("deleteDialog.description", { title: chat.title })}</DialogDescription>
      </DialogHeader>
      {remove.isError && <p role="alert" className="text-xs text-destructive">{errorText(remove.error)}</p>}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>{common("cancel")}</Button>
        <Button type="button" variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
          {remove.isPending ? t("deleteDialog.deleting") : t("delete")}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}
