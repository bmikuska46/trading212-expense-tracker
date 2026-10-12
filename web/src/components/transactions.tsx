"use client";

import Link from "next/link";
import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { keepPreviousData, queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useHotkey } from "@tanstack/react-hotkeys";
import {
  rowSelectionFeature, rowSortingFeature, tableFeatures, useTable,
  type ColumnDef, type RowSelectionState, type SortingState,
} from "@tanstack/react-table";
import { useCallback, useMemo, useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { ArrowDownToLine, ChevronLeft, ChevronRight, CornerDownLeft, Search, Sparkles, X } from "lucide-react";
import {
  ApiError, bulkTransactions, categorizeTransactions, getStatistics, listAccounts, listCategories, listIntegrations,
  listTransactions, registerSearch, ledgerQueries,
  type Account, type BulkInput, type Entity, type RegisterQuery, type RuleSuggestion, type Transaction,
} from "@/lib/api";
import { formatEURCents, formatNumber } from "@/lib/format";
import { useSession } from "@/lib/session";
import { dialogOpen } from "@/components/keyboard-shortcuts";
import { ConfirmImport } from "@/components/notifications";
import { PageToolbar } from "@/components/page-toolbar";
import { cellClass, headClass } from "@/components/panel";
import { ariaSort, SortButton } from "@/components/sortable";
import { Amount, CategoryChip, categoryText, payeeText, shortDate, TransactionBadges, type TransactionsT } from "@/components/transaction-bits";
import { kinds, RuleOffer, TransactionEditor, type EditorTarget } from "@/components/transaction-editor";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Card, CardFooter } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput, InputGroupText } from "@/components/ui/input-group";
import { Kbd, KbdGroup } from "@/components/ui/kbd";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

const filterKeys = ["q", "from", "to", "account", "category", "payee", "batch", "kind", "review", "sort", "dir", "page", "pageSize"] as const;
const filterFields = new Set(["q", "from", "to", "account", "category", "kind", "review"]);
const sortable = new Set(["date", "amount", "payee", "category", "account"]);

function readQuery(params: URLSearchParams): RegisterQuery {
  const query: RegisterQuery = {};
  for (const key of filterKeys) {
    const value = params.get(key);
    if (value) query[key] = value;
  }
  return query;
}

// The summary ignores paging and sorting so it describes every match.
function summaryQuery(query: RegisterQuery): RegisterQuery {
  const rest = { ...query };
  for (const key of ["sort", "dir", "page", "pageSize"] as const) delete rest[key];
  return rest;
}

const features = tableFeatures({ rowSelectionFeature, rowSortingFeature });

// The console table keeps every column on wide screens. Narrow screens drop
// the secondary ones and the payee cell repeats what they held.
const columnClass: Record<string, string> = {
  select: "w-px",
  date: "w-px max-sm:hidden",
  payee: "min-w-40 whitespace-normal",
  category: "w-px max-md:hidden",
  account: "w-px whitespace-nowrap max-lg:hidden",
  notes: "max-2xl:hidden",
  amount: "w-px",
};

function columns(t: TransactionsT, kindLabel: (kind: Transaction["kind"]) => string, edit: (id: number) => void): ColumnDef<typeof features, Transaction>[] {
  return [
    {
      id: "select", enableSorting: false,
      header: ({ table }) => <Checkbox aria-label={t("columns.selectAll")}
        checked={table.getIsAllPageRowsSelected() ? true : table.getIsSomePageRowsSelected() ? "indeterminate" : false}
        onCheckedChange={(value) => table.toggleAllPageRowsSelected(value === true)} />,
      cell: ({ row }) => <Checkbox aria-label={t("columns.selectRow", { date: row.original.date, payee: row.original.payeeName || kindLabel(row.original.kind) })}
        checked={row.getIsSelected()} onCheckedChange={(value) => row.toggleSelected(value === true)} />,
    },
    {
      id: "date", accessorKey: "date", header: t("columns.date"),
      cell: ({ row }) => <time dateTime={row.original.date} className="font-mono text-xs whitespace-nowrap text-faint">{shortDate(row.original.date)}</time>,
    },
    {
      id: "payee", accessorKey: "payeeName", header: t("columns.payee"),
      cell: ({ row }) => <>
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <button type="button" data-row-action onClick={() => edit(row.original.id)}
            className={cn("rounded-sm text-left font-medium underline-offset-4 outline-none hover:underline focus-visible:underline",
              !row.original.payeeName && row.original.kind !== "transfer" && "text-muted-foreground")}>
            {payeeText(t, row.original)}
            <span className="sr-only">{t("columns.editHint")}</span>
          </button>
          <TransactionBadges row={row.original} />
        </span>
        <span className="mt-0.5 block text-xs text-faint lg:hidden">
          <span className="font-mono sm:hidden">{shortDate(row.original.date)}, </span>
          <span className="md:hidden">{categoryText(t, row.original)}, </span>
          {row.original.accountName}
        </span>
      </>,
    },
    { id: "category", accessorKey: "categoryName", header: t("columns.category"), cell: ({ row }) => <CategoryChip row={row.original} /> },
    {
      id: "account", accessorKey: "accountName", header: t("columns.account"),
      cell: ({ row }) => <span className="inline-flex items-center gap-1.5 text-muted-foreground">
        {row.original.accountName}
        {row.original.imported && <><ArrowDownToLine className="size-3 text-faint" aria-hidden="true" /><span className="sr-only">{t("columns.importedHint")}</span></>}
      </span>,
    },
    {
      id: "notes", accessorKey: "notes", header: t("columns.notes"), enableSorting: false,
      cell: ({ row }) => <span className="line-clamp-1 max-w-64 text-xs text-muted-foreground">{row.original.notes}</span>,
    },
    { id: "amount", accessorKey: "amount", header: t("columns.amount"), cell: ({ row }) => <Amount row={row.original} className="text-[0.8125rem]" /> },
  ];
}

function Summary({ query }: { query: RegisterQuery }) {
  const t = useTranslations("transactions.summary");
  const summary = summaryQuery(query);
  const stats = useQuery({
    queryKey: ["statistics", summary],
    queryFn: ({ signal }) => getStatistics(summary, { signal }),
    placeholderData: keepPreviousData,
  });
  if (!stats.data) {
    // The register reports invalid filters, which fail here the same way.
    if (!stats.isError || stats.error instanceof ApiError && Object.keys(stats.error.fields).length > 0) return null;
    return <p role="alert" className="text-destructive">{stats.error.message}</p>;
  }
  const top = stats.data.categories.filter((item) => item.spendingEurCents > 0).slice(0, 5);
  const cells = [
    { label: t("income"), value: formatEURCents(stats.data.incomeEurCents), swatch: "bg-income" },
    {
      label: t("spending"), value: formatEURCents(stats.data.spendingEurCents), swatch: "bg-spending",
      detail: stats.data.refundEurCents > 0 ? t("afterRefunds", { amount: formatEURCents(stats.data.refundEurCents) }) : undefined,
    },
    { label: t("net"), value: formatEURCents(stats.data.netEurCents, { signed: true }) },
    { label: t("transfers"), value: formatNumber(stats.data.transferCount), detail: t("notCounted") },
  ];
  return <Card size="sm" className="gap-0 py-0">
    <h2 className="sr-only">{t("heading")}</h2>
    <dl className="grid grid-cols-2 sm:grid-cols-4">
      {cells.map((cell, index) => <div key={cell.label}
        className={cn("flex flex-col gap-0.5 px-3.5 py-2.5", index % 2 === 1 && "border-l", index > 1 && "max-sm:border-t", index === 2 && "sm:border-l")}>
        <dt className="flex items-center gap-1.5 text-xs text-muted-foreground">
          {cell.swatch && <span aria-hidden="true" className={cn("size-2 rounded-[2px]", cell.swatch)} />}{cell.label}
        </dt>
        <dd className="font-mono text-base font-medium tracking-tight">{cell.value}</dd>
        {cell.detail && <dd className="truncate font-mono text-[0.6875rem] text-faint">{cell.detail}</dd>}
      </div>)}
    </dl>
    {top.length > 0 && <CardFooter className="flex-wrap gap-x-4 gap-y-1 bg-transparent px-3.5 py-2 text-xs">
      <h3 className="font-mono text-[0.625rem] tracking-wider text-faint uppercase">{t("topSpending")}</h3>
      <ul className="contents">
        {top.map((item) => <li key={item.categoryId ?? item.categoryName} className="text-muted-foreground">
          {item.categoryName} <span className="font-mono text-foreground">{formatEURCents(item.spendingEurCents)}</span>
        </li>)}
      </ul>
    </CardFooter>}
  </Card>;
}

type FilterValues = { q: string; from: string; to: string; account: string; category: string; kind: string; review: string };

const registerPage = (query: RegisterQuery) => queryOptions({
  queryKey: ["transactions", query],
  queryFn: ({ signal }) => listTransactions(query, { signal }),
  placeholderData: keepPreviousData,
});

// invalidFields reads the filters the server rejected from a register error.
const invalidFields = (error: Error | null) => error instanceof ApiError ? error.fields : {};

function Filters({ query, accounts, categories, apply }: {
  query: RegisterQuery; accounts: Account[]; categories: Entity[]; apply: (changes: Partial<RegisterQuery>) => void;
}) {
  const t = useTranslations("transactions.filters");
  const kind = useTranslations("common.kinds");
  const current = { q: query.q ?? "", from: query.from ?? "", to: query.to ?? "", account: query.account ?? "",
    category: query.category ?? "", kind: query.kind ?? "", review: query.review ?? "" };
  const form = useForm<FilterValues>({ values: current });
  const submit = form.handleSubmit((values) => apply({ ...values, page: undefined }));
  // Selects apply at once; text and dates apply on submit or blur.
  const select = (name: keyof FilterValues) => form.register(name, { onChange: () => void submit() });
  // The register query reports filters from the address that the server rejects.
  const page = useQuery(registerPage(query));
  const fields: Partial<Record<string, string>> = invalidFields(page.error);
  const invalid = (name: keyof FilterValues) => ({
    "aria-invalid": Boolean(fields[name]),
    "aria-describedby": fields[name] ? `filter-${name}-error` : undefined,
  });
  const error = (name: keyof FilterValues) => fields[name] && <FieldError id={`filter-${name}-error`} className="text-xs">{fields[name]}</FieldError>;
  const filtered = Object.values(summaryQuery(query)).some(Boolean);
  const field = "w-auto shrink-0 gap-1";
  return <form role="search" aria-label={t("label")} className="flex flex-wrap items-start gap-2 border-b px-3.5 py-2.5"
    onSubmit={(event) => { event.preventDefault(); void submit(); }}>
    <Field className="w-full gap-1 sm:w-52 2xl:w-80">
      <FieldLabel htmlFor="filter-q" className="sr-only">{t("search")}</FieldLabel>
      <InputGroup className="h-7">
        <InputGroupAddon><Search aria-hidden="true" /></InputGroupAddon>
        <InputGroupInput id="filter-q" type="search" maxLength={100} placeholder={t("searchPlaceholder")}
          aria-keyshortcuts="/" {...invalid("q")} {...form.register("q")} />
        <InputGroupAddon align="inline-end">
          <Kbd className="max-md:hidden">/</Kbd>
          {/* Several fields block implicit submission, so Enter needs a submit button. */}
          <InputGroupButton type="submit" size="icon-xs" aria-label={t("apply")}><CornerDownLeft aria-hidden="true" /></InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
      {error("q")}
    </Field>
    {/* Phones scroll the remaining filters sideways instead of stacking them. */}
    <div className="flex items-start gap-2 max-sm:-mx-3.5 max-sm:w-[calc(100%+1.75rem)] max-sm:overflow-x-auto max-sm:px-3.5 max-sm:pb-0.5 sm:contents">
      <Field className={field}>
        <InputGroup className="h-7 w-auto">
          <InputGroupAddon><InputGroupText className="font-mono text-[0.6875rem] text-faint uppercase">{t("from")}</InputGroupText></InputGroupAddon>
          <FieldLabel htmlFor="filter-from" className="sr-only">{t("from")}</FieldLabel>
          <InputGroupInput id="filter-from" type="date" className="w-32 flex-none px-1 font-mono text-xs" {...invalid("from")}
            {...form.register("from", { onBlur: () => void submit() })} />
          <span aria-hidden="true" className="font-mono text-[0.6875rem] text-faint uppercase">{t("to")}</span>
          <FieldLabel htmlFor="filter-to" className="sr-only">{t("toLabel")}</FieldLabel>
          <InputGroupInput id="filter-to" type="date" className="w-32 flex-none px-1 font-mono text-xs" {...invalid("to")}
            {...form.register("to", { onBlur: () => void submit() })} />
        </InputGroup>
        {error("from")}
        {error("to")}
      </Field>
      <Field className={field}>
        <FieldLabel htmlFor="filter-account" className="sr-only">{t("account")}</FieldLabel>
        <NativeSelect id="filter-account" size="sm" className="w-36" {...invalid("account")} {...select("account")}>
          <NativeSelectOption value="">{t("allAccounts")}</NativeSelectOption>
          {accounts.map((account) => <NativeSelectOption key={account.id} value={account.id}>{account.name}</NativeSelectOption>)}
        </NativeSelect>
        {error("account")}
      </Field>
      <Field className={field}>
        <FieldLabel htmlFor="filter-category" className="sr-only">{t("category")}</FieldLabel>
        <NativeSelect id="filter-category" size="sm" className="w-40" {...invalid("category")} {...select("category")}>
          <NativeSelectOption value="">{t("allCategories")}</NativeSelectOption>
          {categories.map((category) => <NativeSelectOption key={category.id} value={category.id}>{category.name}</NativeSelectOption>)}
        </NativeSelect>
        {error("category")}
      </Field>
      <Field className={field}>
        <FieldLabel htmlFor="filter-kind" className="sr-only">{t("type")}</FieldLabel>
        <NativeSelect id="filter-kind" size="sm" className="w-32" {...invalid("kind")} {...select("kind")}>
          <NativeSelectOption value="">{t("allTypes")}</NativeSelectOption>
          {kinds.map((value) => <NativeSelectOption key={value} value={value}>{kind(value)}</NativeSelectOption>)}
        </NativeSelect>
        {error("kind")}
      </Field>
      <Field className={field}>
        <FieldLabel htmlFor="filter-review" className="sr-only">{t("review")}</FieldLabel>
        <NativeSelect id="filter-review" size="sm" className="w-36" {...invalid("review")} {...select("review")}>
          <NativeSelectOption value="">{t("anyReview")}</NativeSelectOption>
          <NativeSelectOption value="needs_review">{t("needsReview")}</NativeSelectOption>
          <NativeSelectOption value="source_conflict">{t("sourceChanged")}</NativeSelectOption>
          <NativeSelectOption value="confirmed">{t("reviewed")}</NativeSelectOption>
        </NativeSelect>
        {error("review")}
      </Field>
      {query.payee && <Button type="button" size="sm" variant="outline" className="shrink-0" onClick={() => apply({ payee: undefined, page: undefined })}>
        {t("onePayee")}<X data-icon="inline-end" aria-hidden="true" /><span className="sr-only">{t("showAllPayees")}</span>
      </Button>}
      {query.batch && <Button type="button" size="sm" variant="outline" className="shrink-0" onClick={() => apply({ batch: undefined, page: undefined })}>
        {t("oneImport")}<X data-icon="inline-end" aria-hidden="true" /><span className="sr-only">{t("showAllImports")}</span>
      </Button>}
      {filtered && <Button type="button" size="sm" variant="ghost" className="shrink-0 text-muted-foreground"
        onClick={() => apply(Object.fromEntries(filterKeys.map((key) => [key, undefined])))}>{t("clearAll")}</Button>}
    </div>
  </form>;
}

type BulkValues = { categoryId: string; payee: string; accountId: string };

function BulkBar({ selected, accounts, categories, clear, report }: {
  selected: Transaction[]; accounts: Account[]; categories: Entity[]; clear: () => void; report: (message: string) => void;
}) {
  const t = useTranslations("transactions.bulk");
  const client = useQueryClient();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const form = useForm<BulkValues>({ defaultValues: { categoryId: "", payee: "", accountId: "" } });
  const mutation = useMutation({
    mutationFn: (input: Omit<BulkInput, "items">) => bulkTransactions({ ...input, items: selected.map(({ id, revision }) => ({ id, revision })) }),
    onSuccess: (changed, input) => {
      for (const key of ledgerQueries) void client.invalidateQueries({ queryKey: [key] });
      form.reset();
      setConfirmDelete(false);
      clear();
      report(t(input.action === "delete" ? "deleted" : "updated", { count: changed }));
    },
    onError: () => setConfirmDelete(false),
  });
  const run = (input: Omit<BulkInput, "items">) => mutation.mutate(input);
  const count = selected.length;
  return <section aria-label={t("label")}
    className="flex flex-col gap-2 border-b bg-mark/5 px-3.5 py-2.5 shadow-[inset_2px_0_0_var(--color-mark)]">
    <div className="flex flex-wrap items-center gap-2">
      <p className="mr-2 font-mono text-xs font-medium">{t.rich("selected", { count, n: (chunks) => <span className="text-mark">{chunks}</span> })}</p>
      <form className="flex items-center" onSubmit={form.handleSubmit((values) => values.categoryId && run({ action: "update", categoryId: Number(values.categoryId) }))}>
        <label htmlFor="bulk-category" className="sr-only">{t("category")}</label>
        <ButtonGroup>
          <NativeSelect id="bulk-category" size="sm" className="w-40" {...form.register("categoryId")}>
            <NativeSelectOption value="">{t("setCategory")}</NativeSelectOption>
            {categories.map((category) => <NativeSelectOption key={category.id} value={category.id}>{category.name}</NativeSelectOption>)}
          </NativeSelect>
          <Button size="sm" type="submit" variant="outline" disabled={mutation.isPending}>{t("set")}</Button>
        </ButtonGroup>
      </form>
      <form className="flex items-center" onSubmit={form.handleSubmit((values) => run({ action: "update", payee: values.payee.trim() }))}>
        <label htmlFor="bulk-payee" className="sr-only">{t("payee")}</label>
        <ButtonGroup>
          <Input id="bulk-payee" className="h-7 w-40" maxLength={100} placeholder={t("newPayee")} autoComplete="off" {...form.register("payee")} />
          <Button size="sm" type="submit" variant="outline" disabled={mutation.isPending}>{t("set")}</Button>
        </ButtonGroup>
      </form>
      <form className="flex items-center" onSubmit={form.handleSubmit((values) => values.accountId && run({ action: "update", accountId: Number(values.accountId) }))}>
        <label htmlFor="bulk-account" className="sr-only">{t("account")}</label>
        <ButtonGroup>
          <NativeSelect id="bulk-account" size="sm" className="w-40" {...form.register("accountId")}>
            <NativeSelectOption value="">{t("moveTo")}</NativeSelectOption>
            {accounts.filter((account) => !account.archived && account.currency === "EUR").map((account) =>
              <NativeSelectOption key={account.id} value={account.id}>{account.name}</NativeSelectOption>)}
          </NativeSelect>
          <Button size="sm" type="submit" variant="outline" disabled={mutation.isPending}>{t("move")}</Button>
        </ButtonGroup>
      </form>
      <Button size="sm" variant="outline" disabled={mutation.isPending} onClick={() => run({ action: "update", reviewed: true })}>{t("markReviewed")}</Button>
      {confirmDelete ? <span className="flex items-center gap-2 text-xs">
        {t("confirmDelete", { count })}
        <Button size="sm" variant="destructive" disabled={mutation.isPending} onClick={() => run({ action: "delete" })}>{t("delete")}</Button>
        <Button size="sm" variant="ghost" onClick={() => setConfirmDelete(false)}>{t("keep")}</Button>
      </span> : <Button size="sm" variant="ghost" className="text-destructive" onClick={() => setConfirmDelete(true)}>{t("delete")}</Button>}
      <Button size="sm" variant="ghost" className="ml-auto text-muted-foreground" onClick={clear}>
        {t("clear")}<Kbd className="max-md:hidden">Esc</Kbd>
      </Button>
    </div>
    <p className="text-xs text-faint">{t("help")}</p>
    {mutation.isError && <p role="alert" className="text-xs text-destructive">{mutation.error.message}</p>}
  </section>;
}

function Register({ query, apply, accounts, categories, edit, report, keyboard }: {
  query: RegisterQuery; apply: (changes: Partial<RegisterQuery>) => void; accounts: Account[]; categories: Entity[];
  edit: (id: number) => void; report: (message: string) => void; keyboard: boolean;
}) {
  const t = useTranslations("transactions");
  const kind = useTranslations("common.kinds");
  const common = useTranslations("common.pagination");
  const page = useQuery(registerPage(query));
  // Selection belongs to the rows on screen, so a new page or filter clears it.
  const queryKey = JSON.stringify(query);
  const [selection, setSelection] = useState<{ key: string; rows: RowSelectionState }>({ key: queryKey, rows: {} });
  const rowSelection = selection.key === queryKey ? selection.rows : {};
  const setRowSelection = (updater: RowSelectionState | ((old: RowSelectionState) => RowSelectionState)) =>
    setSelection({ key: queryKey, rows: typeof updater === "function" ? updater(rowSelection) : updater });
  const sorting: SortingState = [{ id: query.sort && sortable.has(query.sort) ? query.sort : "date", desc: (query.dir ?? "desc") !== "asc" }];
  const rows = page.data?.transactions ?? [];
  // Cell renderers are components, so they must keep their identity across
  // renders or every cell remounts and the focused row loses focus.
  const columnDefs = useMemo(() => columns(t, kind, edit), [t, kind, edit]);
  const table = useTable({
    features, columns: columnDefs, data: rows,
    getRowId: (row) => String(row.id),
    manualSorting: true, enableSortingRemoval: false, enableMultiSort: false,
    state: { rowSelection, sorting },
    onRowSelectionChange: setRowSelection,
    onSortingChange: (updater) => {
      const next = typeof updater === "function" ? updater(sorting) : updater;
      const [first] = next;
      if (first) apply({ sort: first.id, dir: first.desc ? "desc" : "asc", page: undefined });
    },
  });
  const selected = rows.filter((row) => rowSelection[String(row.id)]);

  // The current row follows focus and the J and K keys. Like the selection,
  // it belongs to the rows on screen.
  const [cursor, setCursor] = useState<{ key: string; index: number } | null>(null);
  const active = cursor?.key === queryKey && cursor.index < rows.length ? cursor.index : null;
  const focusRow = (index: number) => {
    setCursor({ key: queryKey, index });
    document.querySelector<HTMLElement>(`#register-rows [data-index="${index}"] [data-row-action]`)?.focus({ focusVisible: true });
  };
  const onKey = (action: () => void) => () => { if (!dialogOpen()) action(); };
  const options = { enabled: keyboard && rows.length > 0 };
  useHotkey("J", onKey(() => focusRow(active === null ? 0 : Math.min(rows.length - 1, active + 1))), options);
  useHotkey("K", onKey(() => focusRow(active === null ? 0 : Math.max(0, active - 1))), options);
  useHotkey("X", onKey(() => {
    const row = active === null ? undefined : rows[active];
    if (!row) return;
    const id = String(row.id);
    setRowSelection((old) => {
      const next = { ...old };
      if (next[id]) delete next[id];
      else next[id] = true;
      return next;
    });
  }), options);
  useHotkey("O", onKey(() => { if (active !== null && rows[active]) edit(rows[active].id); }), options);
  const turn = (by: number) => {
    if (!page.data) return;
    const target = page.data.page + by;
    if (target >= 1 && target <= Math.ceil(page.data.total / page.data.pageSize)) apply({ page: target > 1 ? String(target) : undefined });
  };
  useHotkey("[", onKey(() => turn(-1)), options);
  useHotkey("]", onKey(() => turn(1)), options);
  useHotkey("Escape", onKey(() => setRowSelection({})), { enabled: keyboard && selected.length > 0, ignoreInputs: true });

  if (page.isPending) return <div role="status" aria-label={t("register.loading")} className="flex flex-col gap-2 p-3.5">
    {Array.from({ length: 8 }, (_, index) => <Skeleton key={index} className="h-7" />)}
  </div>;
  if (page.isError) {
    const fields = Object.keys(invalidFields(page.error));
    if (fields.length === 0) return <p role="alert" className="p-3.5 text-sm text-destructive">{page.error.message}</p>;
    // Sorting, paging, payee and import settings have no field to highlight.
    const hidden = fields.some((name) => !filterFields.has(name));
    return <div role="alert" className="flex flex-wrap items-center gap-3 p-3.5 text-sm text-destructive">
      <p>{hidden ? t("register.invalidAddress") : t("register.fixFilters")}</p>
      {hidden && <Button variant="outline" size="sm" onClick={() => apply(Object.fromEntries(filterKeys.map((key) => [key, undefined])))}>{t("register.clearAll")}</Button>}
    </div>;
  }
  const { total, pageSize } = page.data;
  const current = page.data.page;
  const pages = Math.max(1, Math.ceil(total / pageSize));
  if (total === 0) {
    return <Empty>
      <EmptyHeader>
        <EmptyTitle>{t("register.emptyTitle")}</EmptyTitle>
        <EmptyDescription>{t.rich("register.emptyBody", { link: (chunks) => <Link href="/imports/">{chunks}</Link> })}</EmptyDescription>
      </EmptyHeader>
    </Empty>;
  }
  const first = (current - 1) * pageSize + 1;
  const last = Math.min(first + rows.length - 1, total);
  return <div className={cn("flex flex-col transition-opacity", page.isPlaceholderData && "opacity-60")} aria-busy={page.isFetching}>
    {selected.length > 0 && <BulkBar selected={selected} accounts={accounts} categories={categories} clear={() => setRowSelection({})} report={report} />}
    <Table className="text-[0.8125rem]">
      <TableCaption className="sr-only">
        {t("register.caption", { first: formatNumber(first), last: formatNumber(last), total })}
      </TableCaption>
      <TableHeader>{table.getHeaderGroups().map((group) => <TableRow key={group.id} className="hover:bg-transparent">
        {group.headers.map((header) => {
          const direction = header.column.getIsSorted();
          const label = <table.FlexRender header={header} />;
          return <TableHead key={header.id} scope="col" className={cn(headClass, header.column.id === "amount" && "text-right", columnClass[header.column.id])}
            aria-sort={ariaSort(direction)}>
            {header.column.getCanSort() ? <SortButton direction={direction} onClick={header.column.getToggleSortingHandler()}>{label}</SortButton> : label}
          </TableHead>;
        })}
      </TableRow>)}</TableHeader>
      <TableBody id="register-rows">{table.getRowModel().rows.map((row, index) => <TableRow key={row.id} data-index={index}
        data-state={row.getIsSelected() ? "selected" : undefined} onFocus={() => setCursor({ key: queryKey, index })}
        className={cn("hover:bg-muted/40 data-[state=selected]:bg-mark/5",
          index === active && "bg-accent shadow-[inset_2px_0_0_var(--color-mark)] hover:bg-accent")}>
        {row.getAllCells().map((cell) => <TableCell key={cell.id} className={cn(cellClass, "align-top", columnClass[cell.column.id])}>
          <table.FlexRender cell={cell} />
        </TableCell>)}
      </TableRow>)}</TableBody>
    </Table>
    <CardFooter className="flex-wrap justify-between gap-3 bg-transparent px-3.5 py-2 text-xs text-muted-foreground">
      <p className="font-mono text-faint">
        {t.rich("register.range", {
          first: formatNumber(first), last: formatNumber(last), total: formatNumber(total),
          b: (chunks) => <span className="text-foreground">{chunks}</span>,
        })}
      </p>
      <p className="flex items-center gap-3 text-faint max-lg:hidden" aria-hidden="true">
        <span className="flex items-center gap-1.5"><KbdGroup><Kbd>J</Kbd><Kbd>K</Kbd></KbdGroup>{t("register.move")}</span>
        <span className="flex items-center gap-1.5"><Kbd>X</Kbd>{t("register.select")}</span>
        <span className="flex items-center gap-1.5"><Kbd>O</Kbd>{t("register.open")}</span>
      </p>
      <nav aria-label={t("register.pages")} className="flex items-center gap-3">
        <span className="flex items-center gap-2">
          <label htmlFor="page-size" className="max-sm:sr-only">{t("register.rows")}</label>
          <NativeSelect id="page-size" size="sm" value={String(pageSize)} onChange={(event) => apply({ pageSize: event.target.value, page: undefined })}>
            {[25, 50, 100, 200].map((size) => <NativeSelectOption key={size} value={size}>{size}</NativeSelectOption>)}
          </NativeSelect>
        </span>
        <span className="font-mono">{formatNumber(current)}/{formatNumber(pages)}</span>
        <ButtonGroup>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button size="icon-sm" variant="outline" aria-label={common("previous")} aria-keyshortcuts="["
                disabled={current <= 1} onClick={() => apply({ page: current - 1 > 1 ? String(current - 1) : undefined })}>
                <ChevronLeft aria-hidden="true" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>{common("previous")} <Kbd>[</Kbd></TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button size="icon-sm" variant="outline" aria-label={common("next")} aria-keyshortcuts="]"
                disabled={current >= pages} onClick={() => apply({ page: String(current + 1) })}>
                <ChevronRight aria-hidden="true" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>{common("next")} <Kbd>]</Kbd></TooltipContent>
          </Tooltip>
        </ButtonGroup>
      </nav>
    </CardFooter>
  </div>;
}

// CategorizeButton asks Jev to categorize every Uncategorized transaction.
// It shows only while TypeSafe is connected and enabled.
function CategorizeButton({ report }: { report: (message: string) => void }) {
  const t = useTranslations("transactions.categorize");
  const client = useQueryClient();
  const integrations = useQuery({ queryKey: ["integrations"], queryFn: listIntegrations });
  const ready = integrations.data?.some((item) => item.provider === "typesafe" && item.configured && item.enabled) ?? false;
  const mutation = useMutation({
    mutationFn: categorizeTransactions,
    onSuccess: ({ categorized, remaining }) => {
      for (const key of ledgerQueries) void client.invalidateQueries({ queryKey: [key] });
      report(categorized === 0 && remaining === 0 ? t("allDone")
        : `${t("done", { count: categorized })}${remaining > 0 ? ` ${t("remaining", { count: remaining })}` : ""}`);
    },
    onError: (error) => report(error.message),
  });
  if (!ready) return null;
  return <Button size="sm" variant="outline" disabled={mutation.isPending} onClick={() => mutation.mutate()}>
    {mutation.isPending ? <Spinner data-icon="inline-start" aria-hidden="true" /> : <Sparkles data-icon="inline-start" aria-hidden="true" />}
    {mutation.isPending ? t("asking") : t("button")}
  </Button>;
}

export function Transactions() {
  const t = useTranslations("transactions");
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname();
  const query = readQuery(params);
  const timezone = useSession().data?.user?.timezone ?? "Europe/Bratislava";
  const accounts = useQuery({ queryKey: ["accounts"], queryFn: listAccounts });
  const categories = useQuery({ queryKey: ["categories"], queryFn: listCategories });
  const [chosen, setChosen] = useState<EditorTarget | null>(null);
  // Other pages open the editor through the address: new=1 records activity
  // and edit=ID opens a transaction. Closing the editor drops the request.
  const [handled, setHandled] = useState<string | null>(null);
  const requested = useMemo((): EditorTarget | null => handled === params.toString() ? null
    : params.get("new") ? { mode: "new" }
    : /^\d+$/.test(params.get("edit") ?? "") ? { mode: "edit", id: Number(params.get("edit")) } : null, [handled, params]);
  const editor = chosen ?? requested;
  const setEditor = useCallback((target: EditorTarget | null) => {
    if (target === null && requested) {
      setHandled(params.toString());
      const rest = new URLSearchParams(params);
      rest.delete("new");
      rest.delete("edit");
      router.replace(`${pathname}${rest.size ? `?${rest}` : ""}`, { scroll: false });
    }
    setChosen(target);
  }, [params, pathname, requested, router]);
  const edit = useCallback((id: number) => setEditor({ mode: "edit", id }), [setEditor]);
  const [status, setStatus] = useState("");
  const [suggestion, setSuggestion] = useState<RuleSuggestion | null>(null);
  useHotkey("N", () => { if (!dialogOpen()) setEditor({ mode: "new" }); }, { enabled: editor === null, conflictBehavior: "allow" });
  useHotkey("/", () => { if (!dialogOpen()) document.getElementById("filter-q")?.focus(); }, { enabled: editor === null, conflictBehavior: "allow" });

  const apply = (changes: Partial<RegisterQuery>) => {
    const next = { ...query, ...changes };
    router.replace(`${pathname}${registerSearch(next)}`, { scroll: false });
  };
  if (accounts.isPending || categories.isPending) return <Skeleton role="status" aria-label={t("register.loading")} className="h-96 rounded-xl" />;
  if (accounts.isError) return <p role="alert" className="text-sm text-destructive">{accounts.error.message}</p>;
  if (categories.isError) return <p role="alert" className="text-sm text-destructive">{categories.error.message}</p>;
  const selectedAccount = query.account && /^\d+$/.test(query.account) ? Number(query.account) : undefined;
  const batch = query.batch && /^\d+$/.test(query.batch) ? Number(query.batch) : undefined;
  return <>
    <PageToolbar><CategorizeButton report={setStatus} /></PageToolbar>
    <p aria-live="polite" className="text-sm text-muted-foreground empty:hidden">{status}</p>
    {batch !== undefined && <ConfirmImport id={batch} report={(message) => setStatus(message)} />}
    {suggestion && <RuleOffer suggestion={suggestion} onDismiss={() => setSuggestion(null)} />}
    <Summary query={query} />
    <Card size="sm" className="gap-0 py-0">
      <h2 className="sr-only">{t("heading")}</h2>
      <Filters query={query} accounts={accounts.data} categories={categories.data} apply={apply} />
      <Register query={query} apply={apply} accounts={accounts.data} categories={categories.data}
        edit={edit} report={setStatus} keyboard={editor === null} />
    </Card>
    <TransactionEditor target={editor} onClose={() => setEditor(null)} accounts={accounts.data} categories={categories.data}
      account={selectedAccount} timezone={timezone}
      onDone={(message, next) => { setEditor(null); setStatus(message); setSuggestion(next); }} />
  </>;
}
