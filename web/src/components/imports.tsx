"use client";

import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { tableFeatures, useTable, type ColumnDef } from "@tanstack/react-table";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useTranslations } from "next-intl";
import { useFieldArray, useForm, useWatch, type Control, type FieldErrors, type UseFormRegister } from "react-hook-form";
import { ArrowLeft, ChevronLeft, ChevronRight, Plus, Sparkles, Trash2 } from "lucide-react";
import {
  commitImport, correctImportRow, deleteImport, getImport, listAccounts, listImportRows, listImports, setImportMapping,
  suggestImportMapping, uploadImport, validateImport,
  ledgerQueries, type Import, type ImportDetail, type ImportSort, type ImportState, type Mapping, type MappingSuggestion, type RowStatus, type StagingRow,
} from "@/lib/api";
import { formatEURCents, formatMoney, formatNumber } from "@/lib/format";
import { showServerErrors } from "@/lib/forms";
import { cn } from "@/lib/utils";
import { useConfirmImport } from "@/components/notifications";
import { alertClass, alertDescriptionClass, cellClass, compactFields, footerClass, headClass, legendClass, PanelHeader } from "@/components/panel";
import { monthName, ScheduledImports } from "@/components/scheduled-imports";
import { nextSort, SortHead, type Sort } from "@/components/sortable";
import { isKind, kinds } from "@/components/transaction-editor";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Skeleton } from "@/components/ui/skeleton";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const maxUploadBytes = 20 * 1024 * 1024;

type T = ReturnType<typeof useTranslations<"imports">>;

function StateBadge({ state }: { state: ImportState }) {
  const t = useTranslations("imports.states");
  const variant = state === "committed" ? "secondary" : state === "ready" ? "default" :
    state === "failed" || state === "needs_attention" ? "destructive" : "warning";
  return <Badge variant={variant}>{t(state)}</Badge>;
}

// period shows the inclusive date range. The server stores an exclusive end.
function period(t: T, start: string | null, end: string | null): string {
  if (!start || !end) return t("periodUnknown");
  const last = new Date(`${end}T00:00:00Z`);
  last.setUTCDate(last.getUTCDate() - 1);
  const through = last.toISOString().slice(0, 10);
  return start === through ? start : t("periodRange", { start, end: through });
}

const linkClass = "font-medium underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring";

function Loading({ label, rows = 4 }: { label: string; rows?: number }) {
  return <div role="status" aria-label={label} className="flex flex-col gap-2 p-3.5">
    {Array.from({ length: rows }, (_, index) => <Skeleton key={index} className="h-6" />)}
  </div>;
}

type UploadValues = { accountId: string; file: FileList | null };

function Upload() {
  const t = useTranslations("imports.upload");
  const client = useQueryClient();
  const router = useRouter();
  const accounts = useQuery({ queryKey: ["accounts"], queryFn: listAccounts });
  const [status, setStatus] = useState("");
  const form = useForm<UploadValues>({ defaultValues: { accountId: "", file: null } });
  const { errors } = form.formState;
  const mutation = useMutation({
    mutationFn: (values: UploadValues) => uploadImport({ accountId: Number(values.accountId), file: values.file![0] }),
    onSuccess: (detail) => {
      void client.invalidateQueries({ queryKey: ["imports"] });
      client.setQueryData(["import", detail.id], detail);
      router.push(`/imports/?batch=${detail.id}`);
    },
    onError: (error) => setStatus(showServerErrors(error, form.setError, ["accountId"])),
  });
  const usable = (accounts.data ?? []).filter((account) => !account.archived);
  const firstAccountId = usable[0]?.id;
  const { getValues, setValue } = form;
  useEffect(() => {
    if (firstAccountId !== undefined && !getValues("accountId")) {
      setValue("accountId", String(firstAccountId));
    }
  }, [firstAccountId, getValues, setValue]);
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="upload-heading" title={t("title")} />
    <CardContent className="px-3.5 py-3">
      {accounts.isPending ? <Loading label={t("loadingAccounts")} rows={1} /> :
        accounts.isError ? <p role="alert" className="text-sm text-destructive">{accounts.error.message}</p> :
        usable.length === 0 ? <Empty className="p-6">
          <EmptyHeader>
            <EmptyTitle>{t("noAccount")}</EmptyTitle>
            <EmptyDescription>{t.rich("noAccountBody", { link: (chunks) => <Link className={linkClass} href="/accounts/">{chunks}</Link> })}</EmptyDescription>
          </EmptyHeader>
        </Empty> :
        <form noValidate aria-labelledby="upload-heading" className="flex flex-col gap-3 sm:flex-row sm:items-start"
          onSubmit={form.handleSubmit((values) => { setStatus(""); mutation.mutate(values); })}>
          <Field data-invalid={Boolean(errors.accountId)} className="gap-1 sm:w-52">
            <FieldLabel htmlFor="import-account" className="text-xs text-muted-foreground">{t("account")}</FieldLabel>
            <NativeSelect id="import-account" size="sm" className="w-full" aria-invalid={Boolean(errors.accountId)}
              aria-describedby={errors.accountId ? "import-account-error" : undefined}
              {...form.register("accountId", { required: t("chooseAccountError") })}>
              <NativeSelectOption value="">{t("chooseAccount")}</NativeSelectOption>
              {usable.map((account) => <NativeSelectOption key={account.id} value={account.id}>{account.name} ({account.currency})</NativeSelectOption>)}
            </NativeSelect>
            {errors.accountId && <FieldError id="import-account-error" className="text-xs">{errors.accountId.message}</FieldError>}
          </Field>
          <Field data-invalid={Boolean(errors.file)} className="gap-1 sm:flex-1">
            <FieldLabel htmlFor="import-file" className="text-xs text-muted-foreground">{t("file")}</FieldLabel>
            <Input id="import-file" type="file" accept=".csv,text/csv" className="h-7 py-0.5 text-xs file:h-5 file:text-xs"
              aria-invalid={Boolean(errors.file)} aria-describedby={errors.file ? "import-file-error" : "import-file-hint"}
              {...form.register("file", {
                validate: (files) => {
                  if (!files || files.length !== 1) return t("chooseFile");
                  if (files[0].size > maxUploadBytes) return t("tooLarge");
                  return true;
                },
              })} />
            {errors.file ? <FieldError id="import-file-error" className="text-xs">{errors.file.message}</FieldError> :
              <FieldDescription id="import-file-hint" className="text-xs">{t("fileHint")}</FieldDescription>}
          </Field>
          <Button type="submit" size="sm" className="sm:mt-5" disabled={mutation.isPending}>{mutation.isPending ? t("uploading") : t("submit")}</Button>
        </form>}
      <p aria-live="polite" className="mt-2 text-xs text-destructive empty:hidden">{status}</p>
    </CardContent>
    <CardFooter className={footerClass}>
      {t("footer")}
    </CardFooter>
  </Card>;
}

const features = tableFeatures({});
const num = (value: number, tone?: string) => <span className={cn("font-mono", tone ?? (value === 0 && "text-faint"))}>{formatNumber(value)}</span>;
const historyColumns = (t: T): ColumnDef<typeof features, Import>[] => [
  // Headers come from the column ids, see imports.history.columns.
  {
    accessorKey: "filename", header: t("history.columns.filename"),
    cell: ({ row }) => <div className="flex min-w-0 flex-col gap-0.5">
      <Link className={`${linkClass} break-all`} href={`/imports/?batch=${row.original.id}`}>{row.original.filename}</Link>
      <span className="text-xs text-faint md:hidden">{row.original.account.name}, {row.original.createdAt.slice(0, 10)}</span>
    </div>,
  },
  { id: "account", header: t("history.columns.account"), cell: ({ row }) => <span className="text-muted-foreground">{row.original.account.name}</span> },
  {
    accessorKey: "state", header: t("history.columns.state"),
    cell: ({ row }) => <div className="flex flex-wrap gap-1">
      <StateBadge state={row.original.state} />
      {row.original.reviewStatus === "needs_review" && <Badge variant="warning">{t("review")}</Badge>}
    </div>,
  },
  { id: "period", header: t("history.columns.period"), cell: ({ row }) => <span className="font-mono text-xs text-muted-foreground">{period(t, row.original.periodStart, row.original.periodEnd)}</span> },
  { accessorKey: "rowCount", header: t("history.columns.rowCount"), cell: ({ row }) => num(row.original.rowCount) },
  { accessorKey: "insertedCount", header: t("history.columns.insertedCount"), cell: ({ row }) => num(row.original.insertedCount) },
  { accessorKey: "duplicateCount", header: t("history.columns.duplicateCount"), cell: ({ row }) => num(row.original.duplicateCount) },
  {
    id: "problems", header: t("history.columns.problems"),
    cell: ({ row }) => {
      const problems = row.original.errorCount + row.original.conflictCount;
      return num(problems, problems > 0 ? "text-destructive" : undefined);
    },
  },
  { accessorKey: "createdAt", header: t("history.columns.createdAt"), cell: ({ row }) => <span className="font-mono text-xs text-faint">{row.original.createdAt.slice(0, 10)}</span> },
];

// Counts line up on the right; the account, period and upload date give way
// on narrow screens, where the file cell repeats the account and date.
const historyClass: Record<string, string> = {
  filename: "min-w-40",
  account: "whitespace-nowrap max-md:hidden",
  state: "w-px",
  period: "w-px whitespace-nowrap max-xl:hidden",
  rowCount: "w-px text-right",
  insertedCount: "w-px text-right max-sm:hidden",
  duplicateCount: "w-px text-right max-lg:hidden",
  problems: "w-px text-right max-sm:hidden",
  createdAt: "w-px whitespace-nowrap max-md:hidden",
};

// Counts sort largest first; names, states and dates start from the top.
const numericSorts = new Set<ImportSort>(["rowCount", "insertedCount", "duplicateCount", "problems", "createdAt", "period"]);

function History() {
  const t = useTranslations("imports");
  const common = useTranslations("common.pagination");
  const columns = useMemo(() => historyColumns(t), [t]);
  const [page, setPage] = useState(1);
  const [sort, setSort] = useState<Sort<ImportSort>>({ key: "createdAt", desc: true });
  const search = { page, sort: sort.key, dir: sort.desc ? "desc" as const : "asc" as const };
  const query = useQuery({ queryKey: ["imports", search], queryFn: ({ signal }) => listImports(search, { signal }), placeholderData: keepPreviousData });
  const table = useTable({ features, columns, data: query.data?.imports ?? [] });
  const pages = query.data ? Math.max(1, Math.ceil(query.data.total / query.data.pageSize)) : 1;
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="imports-heading" title={t("history.title")} count={query.data && formatNumber(query.data.total)} />
    {query.isPending ? <Loading label={t("history.loading")} /> :
      query.isError ? <p role="alert" className="px-3.5 py-3 text-sm text-destructive">{query.error.message}</p> :
      query.data.total === 0 ? <Empty className="py-10">
        <EmptyHeader>
          <EmptyTitle>{t("history.empty")}</EmptyTitle>
          <EmptyDescription>{t("history.emptyBody")}</EmptyDescription>
        </EmptyHeader>
      </Empty> :
      <div aria-busy={query.isPlaceholderData} className={cn("transition-opacity", query.isPlaceholderData && "opacity-60")}>
        <Table>
          <TableCaption className="sr-only">{t("history.kept")}</TableCaption>
          <TableHeader>{table.getHeaderGroups().map((group) => <TableRow key={group.id} className="hover:bg-transparent">
            {group.headers.map((header) => <SortHead key={header.id} sort={sort} sortKey={header.column.id as ImportSort}
              onSort={(key) => { setSort(nextSort(sort, key, numericSorts.has(key))); setPage(1); }} className={historyClass[header.column.id]}>
              <table.FlexRender header={header} />
            </SortHead>)}
          </TableRow>)}</TableHeader>
          <TableBody>{table.getRowModel().rows.map((row) => <TableRow key={row.id} className="hover:bg-muted/40">
            {row.getAllCells().map((cell) => <TableCell key={cell.id} className={cn(cellClass, "align-top", historyClass[cell.column.id])}>
              <table.FlexRender cell={cell} />
            </TableCell>)}
          </TableRow>)}</TableBody>
        </Table>
      </div>}
    <CardFooter className={cn(footerClass, "flex-wrap justify-between gap-3 py-2")}>
      <span>{t("history.kept")}</span>
      {pages > 1 && <nav aria-label={t("history.pages")} className="flex items-center gap-3">
        <span className="font-mono">{page}/{pages}</span>
        <ButtonGroup>
          <Button size="icon-sm" variant="outline" aria-label={common("previous")} disabled={page <= 1} onClick={() => setPage(page - 1)}>
            <ChevronLeft aria-hidden="true" />
          </Button>
          <Button size="icon-sm" variant="outline" aria-label={common("next")} disabled={page >= pages} onClick={() => setPage(page + 1)}>
            <ChevronRight aria-hidden="true" />
          </Button>
        </ButtonGroup>
      </nav>}
    </CardFooter>
  </Card>;
}

// Draft holds the mapping as form text. Column selects use the column index
// as text and an empty string for no column.
type Draft = {
  dateColumn: string; dateFormat: string; timezone: string;
  amountMode: string; amountColumn: string; debitColumn: string; creditColumn: string;
  sign: string; decimalSeparator: string; thousandsSeparator: string;
  currencySource: "fixed" | "column"; currencyFixed: string; currencyColumn: string;
  externalIdColumn: string; payeeColumn: string; categoryColumn: string; notesColumn: string; notesFallbackColumn: string;
  actionColumn: string; feeAmountColumn: string; feeCurrencyColumn: string;
  actions: { value: string; kind: string }[];
};

type OptionalColumn = "externalIdColumn" | "payeeColumn" | "categoryColumn" | "notesColumn" | "notesFallbackColumn" | "feeAmountColumn" | "feeCurrencyColumn";
const optionalColumns: OptionalColumn[] = ["externalIdColumn", "payeeColumn", "categoryColumn", "notesColumn", "notesFallbackColumn", "feeAmountColumn", "feeCurrencyColumn"];

// A file without a header row gets numbered column names from the server.
const isHeaderless = (header: string[]) => header.every((name, index) => name === `Column ${index + 1}`);

const normalize = (value: string) => value.trim().replace(/\s+/g, " ").toLowerCase();
const columnText = (column: number | null) => column === null ? "" : String(column);
const columnNumber = (value: string) => value === "" ? null : Number(value);

function toDraft(mapping: Mapping | null, unknown: Import["unknownActions"]): Draft {
  const actions = (mapping?.actions ?? []).map((rule) => ({ value: rule.value, kind: rule.kind }));
  for (const item of unknown) {
    if (!actions.some((rule) => normalize(rule.value) === normalize(item.value))) actions.push({ value: item.value, kind: "" });
  }
  return {
    dateColumn: columnText(mapping?.date.column ?? null),
    dateFormat: mapping?.date.format ?? "iso",
    timezone: mapping?.date.timezone ?? "local",
    amountMode: mapping?.amount.mode ?? "single",
    amountColumn: columnText(mapping?.amount.column ?? null),
    debitColumn: columnText(mapping?.amount.debitColumn ?? null),
    creditColumn: columnText(mapping?.amount.creditColumn ?? null),
    sign: mapping?.amount.sign || "negative_is_outflow",
    decimalSeparator: mapping?.amount.decimalSeparator ?? ".",
    thousandsSeparator: mapping?.amount.thousandsSeparator ?? "",
    currencySource: mapping?.currency.column != null ? "column" : "fixed",
    currencyFixed: mapping?.currency.fixed ?? "EUR",
    currencyColumn: columnText(mapping?.currency.column ?? null),
    externalIdColumn: columnText(mapping?.externalIdColumn ?? null),
    payeeColumn: columnText(mapping?.payeeColumn ?? null),
    categoryColumn: columnText(mapping?.categoryColumn ?? null),
    notesColumn: columnText(mapping?.notesColumn ?? null),
    notesFallbackColumn: columnText(mapping?.notesFallbackColumn ?? null),
    actionColumn: columnText(mapping?.actionColumn ?? null),
    feeAmountColumn: columnText(mapping?.feeAmountColumn ?? null),
    feeCurrencyColumn: columnText(mapping?.feeCurrencyColumn ?? null),
    actions,
  };
}

function fromDraft(draft: Draft): Mapping {
  const single = draft.amountMode === "single";
  return {
    date: { column: Number(draft.dateColumn), format: draft.dateFormat, timezone: draft.timezone },
    amount: {
      mode: draft.amountMode,
      column: single ? columnNumber(draft.amountColumn) : null,
      debitColumn: single ? null : columnNumber(draft.debitColumn),
      creditColumn: single ? null : columnNumber(draft.creditColumn),
      sign: draft.sign, decimalSeparator: draft.decimalSeparator, thousandsSeparator: draft.thousandsSeparator,
    },
    currency: draft.currencySource === "fixed" ? { column: null, fixed: draft.currencyFixed.trim().toUpperCase() } :
      { column: columnNumber(draft.currencyColumn), fixed: null },
    externalIdColumn: columnNumber(draft.externalIdColumn),
    payeeColumn: columnNumber(draft.payeeColumn),
    categoryColumn: columnNumber(draft.categoryColumn),
    notesColumn: columnNumber(draft.notesColumn),
    notesFallbackColumn: columnNumber(draft.notesFallbackColumn),
    actionColumn: columnNumber(draft.actionColumn),
    feeAmountColumn: columnNumber(draft.feeAmountColumn),
    feeCurrencyColumn: columnNumber(draft.feeCurrencyColumn),
    actions: draft.actionColumn === "" ? [] : draft.actions.map((rule) => ({ value: rule.value.trim(), kind: rule.kind })),
    ambiguities: [],
  };
}

type Report = (message: string, error?: boolean) => void;

// columnLabels names each column by its header and its first value, so a
// column reads as "Total: -12.40". A file without a header row only has
// numbered names from the server, so its columns go by their values alone.
function columnLabels(t: T, header: string[], examples: string[]) {
  const headerless = isHeaderless(header);
  return header.map((name, index) => {
    const example = examples[index] ?? "";
    const value = example.length > 40 ? `${example.slice(0, 39)}…` : example;
    if (headerless) return `${index + 1}. ${value || t("emptyValue")}`;
    return `${index + 1}. ${name || t("noName")}${value ? `: ${value}` : ""}`;
  });
}

function ColumnSelect({ id, columns, label, hint, error, none, register, name, required }: {
  id: string; columns: string[]; label: string; hint?: string; error?: string; none?: string;
  register: UseFormRegister<Draft>; name: keyof Omit<Draft, "actions" | "currencySource">; required?: string;
}) {
  const t = useTranslations("imports");
  const describedBy = error ? `${id}-error` : hint ? `${id}-hint` : undefined;
  return <Field data-invalid={Boolean(error)}>
    <FieldLabel htmlFor={id}>{label}</FieldLabel>
    <NativeSelect size="sm" id={id} className="w-full" aria-invalid={Boolean(error)} aria-describedby={describedBy}
      {...register(name, required ? { required } : undefined)}>
      <NativeSelectOption value="">{none ?? t("notInFile")}</NativeSelectOption>
      {columns.map((column, index) => <NativeSelectOption key={index} value={String(index)}>{column}</NativeSelectOption>)}
    </NativeSelect>
    {error ? <FieldError id={`${id}-error`}>{error}</FieldError> : hint && <FieldDescription id={`${id}-hint`}>{hint}</FieldDescription>}
  </Field>;
}

function Actions({ control, register, errors }: {
  control: Control<Draft>; register: UseFormRegister<Draft>; errors: FieldErrors<Draft>;
}) {
  const t = useTranslations("imports.actions");
  const kind = useTranslations("common.kinds");
  const actions = useFieldArray({ control, name: "actions" });
  return <fieldset className="flex flex-col gap-2">
    <legend className={legendClass}>{t("legend")}</legend>
    <p className="text-xs text-muted-foreground">{t("intro")}</p>
    {actions.fields.map((item, index) => {
      const valueError = errors.actions?.[index]?.value?.message;
      const kindError = errors.actions?.[index]?.kind?.message;
      return <div key={item.id} className="grid gap-2 sm:grid-cols-[1fr_12rem_auto] sm:items-start">
        <Field data-invalid={Boolean(valueError)}>
          <FieldLabel htmlFor={`action-value-${index}`} className="sr-only">{t("value", { number: index + 1 })}</FieldLabel>
          <Input id={`action-value-${index}`} maxLength={200} className="h-7 text-[0.8125rem]" aria-invalid={Boolean(valueError)} placeholder={t("valuePlaceholder")}
            {...register(`actions.${index}.value`, { validate: (value) => value.trim() !== "" || t("enterValue") })} />
          {valueError && <FieldError>{valueError}</FieldError>}
        </Field>
        <Field data-invalid={Boolean(kindError)}>
          <FieldLabel htmlFor={`action-kind-${index}`} className="sr-only">{t("meaning", { number: index + 1 })}</FieldLabel>
          <NativeSelect size="sm" id={`action-kind-${index}`} className="w-full" aria-invalid={Boolean(kindError)}
            {...register(`actions.${index}.kind`, { required: t("chooseMeaningError") })}>
            <NativeSelectOption value="">{t("chooseMeaning")}</NativeSelectOption>
            {kinds.map((value) => <NativeSelectOption key={value} value={value}>{kind(value)}</NativeSelectOption>)}
            <NativeSelectOption value="skip">{t("skip")}</NativeSelectOption>
          </NativeSelect>
          {kindError && <FieldError>{kindError}</FieldError>}
        </Field>
        <Button type="button" variant="ghost" size="icon-sm" aria-label={t("remove", { number: index + 1 })} onClick={() => actions.remove(index)}>
          <Trash2 aria-hidden="true" />
        </Button>
      </div>;
    })}
    <Button type="button" variant="outline" size="xs" className="w-fit" onClick={() => actions.append({ value: "", kind: "" })}>
      <Plus data-icon="inline-start" aria-hidden="true" />{t("add")}
    </Button>
  </fieldset>;
}

type EditorProps = { detail: ImportDetail; onSaved: (detail: ImportDetail) => void; report: Report };

// A file in a new format asks Jev for a mapping as soon as it is staged,
// once per import, so the form opens with the suggested columns selected.
// The answer is cached for the session and nothing is saved until the owner
// reviews it and saves the mapping.
function MappingEditor(props: EditorProps) {
  const t = useTranslations("imports.mapping");
  const { detail } = props;
  const auto = detail.canSuggest && !detail.mapping;
  const suggestion = useQuery({
    queryKey: ["import-suggestion", detail.id],
    queryFn: () => suggestImportMapping(detail.id),
    enabled: auto, staleTime: Infinity, retry: false, refetchOnWindowFocus: false,
  });
  if (auto && suggestion.isPending) return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="mapping-heading" title={t("title")} count={t("newFormat")} />
    <CardContent className="flex flex-col gap-3 px-3.5 py-3">
      <p role="status" className="flex items-center gap-2 text-xs text-muted-foreground">
        <Spinner aria-hidden="true" className="size-3.5" />
        {t("asking")}
      </p>
      <div className="grid gap-3 sm:grid-cols-3">
        {Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-12" />)}
      </div>
    </CardContent>
  </Card>;
  return <MappingForm {...props} suggested={auto ? suggestion.data : undefined}
    suggestError={auto && suggestion.isError ? suggestion.error.message : undefined} />;
}

function MappingForm({ detail, onSaved, report, suggested, suggestError }: EditorProps & { suggested?: MappingSuggestion; suggestError?: string }) {
  const all = useTranslations("imports");
  const t = useTranslations("imports.mapping");
  const common = useTranslations("common");
  const client = useQueryClient();
  const needsWork = detail.state === "needs_mapping" || detail.unknownActions.length > 0;
  const [open, setOpen] = useState(needsWork);
  const [filled, setFilled] = useState(suggested);
  const form = useForm<Draft>({ defaultValues: toDraft(suggested?.mapping ?? detail.mapping, detail.unknownActions) });
  const { errors } = form.formState;
  const [amountMode, currencySource, actionColumn] = useWatch({ control: form.control, name: ["amountMode", "currencySource", "actionColumn"] });
  const save = useMutation({
    mutationFn: (draft: Draft) => setImportMapping(detail.id, fromDraft(draft)),
    onSuccess: (next) => { onSaved(next); report(t("saved")); },
    onError: (error) => report(error.message, true),
  });
  const suggest = useMutation({
    mutationFn: () => suggestImportMapping(detail.id),
    onSuccess: (next) => {
      client.setQueryData(["import-suggestion", detail.id], next);
      form.reset(toDraft(next.mapping, detail.unknownActions));
      setOpen(true);
      setFilled(next);
      report(t("suggested"));
    },
    onError: (error) => report(error.message, true),
  });
  const header = detail.header;
  const headerless = isHeaderless(header);
  const columns = columnLabels(all, header, detail.examples);
  const busy = save.isPending || suggest.isPending;
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="mapping-heading" title={t("title")} count={detail.mapping ? `v${detail.mappingVersion ?? 1}` : t("newFormat")} action={<>
      {!open && <Button size="xs" variant="outline" onClick={() => setOpen(true)}>{t("change")}</Button>}
      {detail.canSuggest && <Button size="xs" variant="outline" disabled={busy} onClick={() => suggest.mutate()}>
        {suggest.isPending ? <Spinner data-icon="inline-start" aria-hidden="true" /> : <Sparkles data-icon="inline-start" aria-hidden="true" />}
        {suggest.isPending ? t("askingShort") : filled || suggestError ? t("suggestAgain") : t("suggest")}
      </Button>}
    </>} />
    <CardContent className="flex flex-col gap-4 px-3.5 py-3">
      {filled && !detail.mapping && <Alert className={alertClass}>
        <Sparkles aria-hidden="true" />
        <AlertTitle>{t("filled")}</AlertTitle>
        <AlertDescription className={alertDescriptionClass}>
          <p>{t("filledBody")}</p>
          {filled.notes.length > 0 && <>
            <p>{t("checkNotes")}</p>
            <ul className="list-disc pl-4">{filled.notes.map((note, index) => <li key={index}>{note}</li>)}</ul>
          </>}
        </AlertDescription>
      </Alert>}
      {suggestError && !filled && <Alert variant="destructive" className={alertClass}>
        <AlertTitle>{t("suggestFailed")}</AlertTitle>
        <AlertDescription className={alertDescriptionClass}>{t("suggestFailedBody", { error: suggestError })}</AlertDescription>
      </Alert>}
      <p className="text-xs text-muted-foreground">
        {headerless && `${t("headerless")} `}
        {detail.mapping ? t("reused") : detail.canSuggest ? t("newAuto") : t("newManual")}
      </p>
      {open && <form noValidate aria-labelledby="mapping-heading" className={cn("flex flex-col gap-5", compactFields)} onSubmit={form.handleSubmit((draft) => save.mutate(draft))}>
        <fieldset className="grid gap-3 sm:grid-cols-3">
          <legend className={cn(legendClass, "mb-2")}>{t("date")}</legend>
          <ColumnSelect id="map-date" columns={columns} label={t("dateColumn")} none={t("chooseColumn")} register={form.register}
            name="dateColumn" required={t("chooseDate")} error={errors.dateColumn?.message} />
          <Field>
            <FieldLabel htmlFor="map-date-format">{t("dateOrder")}</FieldLabel>
            <NativeSelect size="sm" id="map-date-format" className="w-full" {...form.register("dateFormat")}>
              <NativeSelectOption value="iso">{t("iso")}</NativeSelectOption>
              <NativeSelectOption value="dmy">{t("dmy")}</NativeSelectOption>
              <NativeSelectOption value="mdy">{t("mdy")}</NativeSelectOption>
            </NativeSelect>
          </Field>
          <Field>
            <FieldLabel htmlFor="map-timezone">{t("timezone")}</FieldLabel>
            <NativeSelect size="sm" id="map-timezone" className="w-full" {...form.register("timezone")}>
              <NativeSelectOption value="local">{t("local")}</NativeSelectOption>
              <NativeSelectOption value="utc">{t("utc")}</NativeSelectOption>
            </NativeSelect>
          </Field>
        </fieldset>
        <fieldset className="grid gap-3 sm:grid-cols-3">
          <legend className={cn(legendClass, "mb-2")}>{t("amount")}</legend>
          <Field>
            <FieldLabel htmlFor="map-amount-mode">{t("amountMode")}</FieldLabel>
            <NativeSelect size="sm" id="map-amount-mode" className="w-full" {...form.register("amountMode")}>
              <NativeSelectOption value="single">{t("single")}</NativeSelectOption>
              <NativeSelectOption value="debit_credit">{t("debitCredit")}</NativeSelectOption>
            </NativeSelect>
          </Field>
          {amountMode === "single" ? <>
            <ColumnSelect id="map-amount" columns={columns} label={t("amountColumn")} none={t("chooseColumn")} register={form.register}
              name="amountColumn" required={t("chooseAmount")} error={errors.amountColumn?.message} />
            <Field>
              <FieldLabel htmlFor="map-sign">{t("sign")}</FieldLabel>
              <NativeSelect size="sm" id="map-sign" className="w-full" {...form.register("sign")}>
                <NativeSelectOption value="negative_is_outflow">{t("negative")}</NativeSelectOption>
                <NativeSelectOption value="positive_is_outflow">{t("positive")}</NativeSelectOption>
                <NativeSelectOption value="action_is_direction">{t("actionDirection")}</NativeSelectOption>
              </NativeSelect>
            </Field>
          </> : <>
            <ColumnSelect id="map-debit" columns={columns} label={t("debit")} none={t("chooseColumn")} register={form.register}
              name="debitColumn" required={t("chooseDebit")} error={errors.debitColumn?.message} />
            <ColumnSelect id="map-credit" columns={columns} label={t("credit")} none={t("chooseColumn")} register={form.register}
              name="creditColumn" required={t("chooseCredit")} error={errors.creditColumn?.message} />
          </>}
          <Field>
            <FieldLabel htmlFor="map-decimal">{t("decimal")}</FieldLabel>
            <NativeSelect size="sm" id="map-decimal" className="w-full" {...form.register("decimalSeparator")}>
              <NativeSelectOption value=".">{t("pointDecimal")}</NativeSelectOption>
              <NativeSelectOption value=",">{t("commaDecimal")}</NativeSelectOption>
            </NativeSelect>
          </Field>
          <Field>
            <FieldLabel htmlFor="map-thousands">{t("thousands")}</FieldLabel>
            <NativeSelect size="sm" id="map-thousands" className="w-full" {...form.register("thousandsSeparator")}>
              <NativeSelectOption value="">{t("none")}</NativeSelectOption>
              <NativeSelectOption value=",">{t("commaThousands")}</NativeSelectOption>
              <NativeSelectOption value=".">{t("pointThousands")}</NativeSelectOption>
              <NativeSelectOption value=" ">{t("space")}</NativeSelectOption>
              <NativeSelectOption value="'">{t("apostrophe")}</NativeSelectOption>
            </NativeSelect>
          </Field>
        </fieldset>
        <fieldset className="grid gap-3 sm:grid-cols-3">
          <legend className={cn(legendClass, "mb-2")}>{t("currency")}</legend>
          <Field>
            <FieldLabel htmlFor="map-currency-source">{t("currencySource")}</FieldLabel>
            <NativeSelect size="sm" id="map-currency-source" className="w-full" {...form.register("currencySource")}>
              <NativeSelectOption value="fixed">{t("fixed")}</NativeSelectOption>
              <NativeSelectOption value="column">{t("fromColumn")}</NativeSelectOption>
            </NativeSelect>
          </Field>
          {currencySource === "fixed" ? <Field data-invalid={Boolean(errors.currencyFixed)}>
            <FieldLabel htmlFor="map-currency">{t("currencyCode")}</FieldLabel>
            <Input id="map-currency" maxLength={3} autoComplete="off" className="h-7 font-mono text-[0.8125rem] uppercase" aria-invalid={Boolean(errors.currencyFixed)}
              {...form.register("currencyFixed", { validate: (value) => /^[A-Za-z]{3}$/.test(value.trim()) || t("enterCode") })} />
            {errors.currencyFixed && <FieldError>{errors.currencyFixed.message}</FieldError>}
          </Field> :
            <ColumnSelect id="map-currency-column" columns={columns} label={t("currencyColumn")} none={t("chooseColumn")} register={form.register}
              name="currencyColumn" required={t("chooseCurrency")} error={errors.currencyColumn?.message} />}
        </fieldset>
        <fieldset className="grid gap-3 sm:grid-cols-3">
          <legend className={cn(legendClass, "mb-2")}>{t("other")}</legend>
          {optionalColumns.map((name) => <ColumnSelect key={name} id={`map-${name}`} columns={columns}
            label={all(`optional.${name}`)} hint={all(`optional.${name}Hint`)} register={form.register} name={name} />)}
          <ColumnSelect id="map-action" columns={columns} label={t("action")} hint={t("actionHint")}
            register={form.register} name="actionColumn" />
        </fieldset>
        {actionColumn !== "" && <Actions control={form.control} register={form.register} errors={errors} />}
        <div className="flex flex-wrap gap-2">
          <Button type="submit" size="sm" disabled={busy}>{save.isPending ? t("saving") : t("save")}</Button>
          {detail.mapping && <Button type="button" size="sm" variant="ghost" disabled={busy}
            onClick={() => { form.reset(toDraft(detail.mapping, detail.unknownActions)); setOpen(needsWork); }}>{common("cancel")}</Button>}
        </div>
      </form>}
    </CardContent>
  </Card>;
}

type CorrectionValues = { values: { text: string }[] };

function Correction({ detail, row, onClose, onSaved }: {
  detail: ImportDetail; row: StagingRow | null; onClose: () => void; onSaved: (detail: ImportDetail, message: string) => void;
}) {
  const t = useTranslations("imports");
  const common = useTranslations("common");
  const [error, setError] = useState("");
  const form = useForm<CorrectionValues>({
    values: { values: (row?.correctedValues ?? row?.values ?? []).map((text) => ({ text })) },
  });
  const mutation = useMutation({
    mutationFn: (values: string[] | null) => correctImportRow(detail.id, row!.row, { excluded: row!.excluded, values }),
    onSuccess: (next, values) => onSaved(next, t(values === null ? "correction.restored" : "correction.corrected", { row: row!.row })),
    onError: (failure) => setError(failure.message),
  });
  return <Dialog open={row !== null} onOpenChange={(open) => { if (!open) { setError(""); onClose(); } }}>
    <DialogContent className="max-h-[calc(100vh-2rem)] overflow-y-auto sm:max-w-2xl">
      <DialogHeader>
        <DialogTitle>{t("correction.title", { row: row?.row ?? "" })}</DialogTitle>
        <DialogDescription>
          {t("correction.description")}
        </DialogDescription>
      </DialogHeader>
      {row && <form noValidate className="flex flex-col gap-4"
        onSubmit={form.handleSubmit((values) => { setError(""); mutation.mutate(values.values.map((item) => item.text)); })}>
        {row.errors.length > 0 && <Alert variant="destructive" className={alertClass}>
          <AlertTitle>{t("correction.problems")}</AlertTitle>
          <AlertDescription className={alertDescriptionClass}><ul className="list-disc pl-5">{row.errors.map((message) => <li key={message}>{message}</li>)}</ul></AlertDescription>
        </Alert>}
        <div className="grid gap-3 sm:grid-cols-2">
          {detail.header.map((name, index) => <Field key={index}>
            <FieldLabel htmlFor={`correct-${index}`}>{name && !isHeaderless(detail.header) ? name : t("column", { number: index + 1 })}</FieldLabel>
            <Input id={`correct-${index}`} maxLength={4000} autoComplete="off" {...form.register(`values.${index}.text`)} />
          </Field>)}
        </div>
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
        <DialogFooter className="sm:justify-between">
          {row.correctedValues ? <Button type="button" variant="ghost" disabled={mutation.isPending} onClick={() => mutation.mutate(null)}>
            {t("correction.useFile")}
          </Button> : <span />}
          <Button type="submit" disabled={mutation.isPending}>{mutation.isPending ? common("saving") : t("correction.save")}</Button>
        </DialogFooter>
      </form>}
    </DialogContent>
  </Dialog>;
}

function DescribeRow({ row, header }: { row: StagingRow; header: string[] }) {
  const t = useTranslations("imports.row");
  const kind = useTranslations("common.kinds");
  // A row with problems may hold a partial record, so show its values instead.
  if (row.record && row.errors.length === 0 && row.record.kind !== "skip") {
    const record = row.record;
    return <div className="flex flex-col gap-0.5">
      <span className="font-medium">{record.payee || record.notes || record.action || t("noDescription")}</span>
      <span className="text-xs text-faint">
        <span className="font-mono">{record.date}</span> · {isKind(record.kind) ? kind(record.kind) : record.kind} · <span className="font-mono text-muted-foreground">{formatMoney(record.amount, record.currency)}</span>
        {record.conversion && <> · {t.rich("conversion", {
          amount: formatEURCents(record.conversion.eurCents), mono, rate: record.conversion.rate, currency: record.currency, date: record.conversion.rateDate,
        })}</>}
      </span>
    </div>;
  }
  if (row.record?.kind === "skip") return <span className="text-muted-foreground">{t("skipped", { action: row.record.action })}</span>;
  const values = row.correctedValues ?? row.values;
  return <span className="font-mono text-xs break-all text-muted-foreground">
    {values.map((value, index) => `${header[index] ?? index + 1}: ${value}`).join("; ")}
  </span>;
}

function RowProblems({ row, className }: { row: StagingRow; className?: string }) {
  const t = useTranslations("imports.row");
  if (row.errors.length > 0) return <ul className={cn("list-disc pl-4 text-xs text-destructive", className)}>{row.errors.map((message) => <li key={message}>{message}</li>)}</ul>;
  if (row.status === "conflict") return <p className={cn("text-xs text-spending", className)}>{t("conflict")}</p>;
  if (row.status === "duplicate") return <p className={cn("text-xs text-faint", className)}>{t("duplicate")}</p>;
  return null;
}

const rowFilters: RowStatus[] = ["invalid", "conflict", "valid", "duplicate", "excluded", "pending"];

function Rows({ detail, onChanged, report }: { detail: ImportDetail; onChanged: (detail: ImportDetail) => void; report: Report }) {
  const t = useTranslations("imports");
  const common = useTranslations("common");
  const client = useQueryClient();
  const [filter, setFilter] = useState<RowStatus | "">(detail.errorCount > 0 ? "invalid" : "");
  const [editing, setEditing] = useState<StagingRow | null>(null);
  const rows = useInfiniteQuery({
    queryKey: ["import-rows", detail.id, filter],
    queryFn: ({ pageParam, signal }) => listImportRows(detail.id, { status: filter || undefined, after: pageParam }, { signal }),
    initialPageParam: 0,
    getNextPageParam: (last) => last.nextAfter ?? undefined,
  });
  const exclude = useMutation({
    mutationFn: (row: StagingRow) => correctImportRow(detail.id, row.row, { excluded: !row.excluded, values: row.correctedValues }),
    onSuccess: (next, row) => {
      onChanged(next);
      void client.invalidateQueries({ queryKey: ["import-rows", detail.id] });
      report(`${t(row.excluded ? "rows.included" : "rows.excluded", { row: row.row })} ${t("rows.checkAgain")}`);
    },
    onError: (error) => report(error.message, true),
  });
  const editable = detail.state !== "committed";
  const items = rows.data?.pages.flatMap((page) => page.rows) ?? [];
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="rows-heading" title={t("rows.title")} count={formatNumber(detail.rowCount)} action={<>
      <label htmlFor="row-filter" className="max-sm:sr-only">{t("rows.show")}</label>
      <NativeSelect id="row-filter" size="sm" className="w-44" value={filter} onChange={(event) => setFilter(event.target.value as RowStatus | "")}>
        <NativeSelectOption value="">{t("rows.all", { count: formatNumber(detail.rowCount) })}</NativeSelectOption>
        {rowFilters.map((status) => <NativeSelectOption key={status} value={status}>
          {t(`rowStates.${status}`)} ({formatNumber(detail.rowCounts[status] ?? 0)})
        </NativeSelectOption>)}
      </NativeSelect>
    </>} />
    {rows.isPending ? <Loading label={t("rows.loading")} /> :
      rows.isError ? <p role="alert" className="px-3.5 py-3 text-sm text-destructive">{rows.error.message}</p> :
      items.length === 0 ? <p className="px-3.5 py-8 text-center text-xs text-muted-foreground">{t("rows.empty")}</p> :
      <Table>
        <TableCaption className="sr-only">{t("rows.numbering")}</TableCaption>
        <TableHeader><TableRow className="hover:bg-transparent">
          <TableHead scope="col" className={cn(headClass, "w-px text-right")}>{t("rows.row")}</TableHead>
          <TableHead scope="col" className={cn(headClass, "w-px")}>{t("rows.status")}</TableHead>
          <TableHead scope="col" className={headClass}>{t("rows.details")}</TableHead>
          <TableHead scope="col" className={cn(headClass, "max-lg:hidden")}>{t("rows.problems")}</TableHead>
          {editable && <TableHead scope="col" className={cn(headClass, "w-px")}><span className="sr-only">{common("actions")}</span></TableHead>}
        </TableRow></TableHeader>
        <TableBody>{items.map((row) => <TableRow key={row.row} className="hover:bg-muted/40">
          <TableCell className={cn(cellClass, "text-right align-top font-mono text-xs text-faint")}>{row.row}</TableCell>
          <TableCell className={cn(cellClass, "align-top")}>
            <Badge variant={row.status === "invalid" || row.status === "conflict" ? "destructive" : row.status === "valid" ? "default" : "outline"}>
              {t(`rowStates.${row.status}`)}
            </Badge>
            {row.correctedValues && <span className="mt-1 block text-xs text-faint">{t("rows.corrected")}</span>}
          </TableCell>
          <TableCell className={cn(cellClass, "max-w-md align-top whitespace-normal")}>
            <DescribeRow row={row} header={detail.header} />
            <RowProblems row={row} className="mt-1 lg:hidden" />
          </TableCell>
          <TableCell className={cn(cellClass, "max-w-sm align-top whitespace-normal max-lg:hidden")}><RowProblems row={row} /></TableCell>
          {editable && <TableCell className={cn(cellClass, "align-top")}>
            <div className="flex justify-end gap-1">
              <Button size="xs" variant="outline" onClick={() => setEditing(row)}>{t("rows.correct")}</Button>
              <Button size="xs" variant="ghost" disabled={exclude.isPending} onClick={() => exclude.mutate(row)}>
                {row.excluded ? t("rows.include") : t("rows.exclude")}
              </Button>
            </div>
          </TableCell>}
        </TableRow>)}</TableBody>
      </Table>}
    <CardFooter className={cn(footerClass, "flex-wrap justify-between gap-3 py-2")}>
      <span>{t("rows.numbering")}</span>
      {rows.hasNextPage && <Button size="xs" variant="outline" disabled={rows.isFetchingNextPage} onClick={() => void rows.fetchNextPage()}>
        {rows.isFetchingNextPage ? common("loading") : t("rows.more")}
      </Button>}
    </CardFooter>
    <Correction key={editing?.row ?? 0} detail={detail} row={editing} onClose={() => setEditing(null)}
      onSaved={(next, message) => {
        setEditing(null);
        onChanged(next);
        void client.invalidateQueries({ queryKey: ["import-rows", detail.id] });
        report(`${message} ${t("rows.checkAgain")}`);
      }} />
  </Card>;
}

function Summary({ detail }: { detail: ImportDetail }) {
  const t = useTranslations("imports");
  const items: { label: string; value: number | string; tone?: string; wide?: boolean }[] = [
    { label: t("summary.period"), value: period(t, detail.periodStart, detail.periodEnd), wide: true },
    { label: t("summary.rows"), value: detail.rowCount },
    { label: detail.state === "committed" ? t("summary.imported") : t("summary.ready"), value: detail.state === "committed" ? detail.insertedCount : detail.rowCounts.valid ?? 0, tone: "text-mark" },
    { label: t("summary.duplicates"), value: detail.duplicateCount },
    { label: t("summary.excluded"), value: detail.excludedCount },
    { label: t("summary.errors"), value: detail.errorCount, tone: detail.errorCount > 0 ? "text-destructive" : undefined },
    { label: t("summary.conflicts"), value: detail.conflictCount, tone: detail.conflictCount > 0 ? "text-spending" : undefined },
  ];
  // The hairlines between cells are the card showing through a 1px gap.
  return <Card size="sm" className="gap-0 py-0">
    <h2 className="sr-only">{t("summary.title")}</h2>
    <dl className="grid grid-cols-2 gap-px bg-border sm:grid-cols-4 xl:grid-cols-8">
      {items.map(({ label, value, tone, wide }) => <div key={label} className={cn("flex flex-col gap-1 bg-card px-3.5 py-2.5", wide && "col-span-2")}>
        <dt className="font-mono text-[0.6875rem] tracking-wider text-faint uppercase">{label}</dt>
        <dd className={cn("font-mono", typeof value === "number" ? "text-base" : "text-[0.8125rem] leading-6", tone,
          value === 0 && "text-faint")}>{typeof value === "number" ? formatNumber(value) : value}</dd>
      </div>)}
    </dl>
  </Card>;
}

const mono = (chunks: ReactNode) => <span className="font-mono">{chunks}</span>;

// committedSummary sums up a committed import in a few sentences.
function committedSummary(t: ReturnType<typeof useTranslations<"imports.detail">>, detail: ImportDetail): string {
  const count = detail.insertedCount;
  const parts = count === 0 ? [t("nothingAdded")] :
    detail.reviewStatus === "confirmed" ? [t("added", { count }),
      detail.confirmedAt ? t("confirmedOn", { count, date: detail.confirmedAt.slice(0, 10) }) : t("confirmed", { count })] :
    [t("addedReview", { count })];
  if (detail.suggestedCategoryCount > 0) {
    parts.push(t("categorized", { count: detail.suggestedCategoryCount }));
    if (detail.reviewStatus === "needs_review") parts.push(t("categorizedReview"));
  }
  if (detail.conflictCount > 0) parts.push(t("conflicts", { count: detail.conflictCount }));
  return parts.join(" ");
}

function Review({ id }: { id: number }) {
  const t = useTranslations("imports.detail");
  const all = useTranslations("imports");
  const client = useQueryClient();
  const router = useRouter();
  const [message, setMessage] = useState<{ text: string; error: boolean }>({ text: "", error: false });
  const [confirmDelete, setConfirmDelete] = useState(false);
  const query = useQuery({ queryKey: ["import", id], queryFn: ({ signal }) => getImport(id, { signal }) });
  const report: Report = (text, error = false) => setMessage({ text, error });
  const changed = (next: ImportDetail) => {
    client.setQueryData(["import", id], next);
    void client.invalidateQueries({ queryKey: ["imports"] });
  };
  const refresh = () => {
    void client.invalidateQueries({ queryKey: ["import", id] });
    void client.invalidateQueries({ queryKey: ["import-rows", id] });
    void client.invalidateQueries({ queryKey: ["imports"] });
  };
  const validate = useMutation({
    mutationFn: () => validateImport(id),
    onSuccess: (next) => {
      changed(next);
      void client.invalidateQueries({ queryKey: ["import-rows", id] });
      report(next.state === "ready" ? t("checked") : t("needsAttention"));
    },
    onError: (error) => report(error.message, true),
  });
  const commit = useMutation({
    mutationFn: () => commitImport(id),
    onSuccess: (next) => {
      changed(next);
      void client.invalidateQueries({ queryKey: ["import-rows", id] });
      for (const key of ledgerQueries) void client.invalidateQueries({ queryKey: [key] });
      report(t("imported", { count: next.insertedCount }));
    },
    // A failed commit may have revalidated the batch, so reload what changed.
    onError: (error) => { refresh(); report(error.message, true); },
  });
  const confirm = useConfirmImport(report);
  const remove = useMutation({
    mutationFn: () => deleteImport(id),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["imports"] });
      client.removeQueries({ queryKey: ["import", id] });
      router.push("/imports/");
    },
    onError: (error) => { setConfirmDelete(false); report(error.message, true); },
  });

  const back = <Button asChild size="xs" variant="ghost" className="-ml-2 w-fit text-muted-foreground">
    <Link href="/imports/"><ArrowLeft data-icon="inline-start" aria-hidden="true" />{t("back")}</Link>
  </Button>;
  if (query.isPending) return <>{back}<Card size="sm" className="py-0"><Loading label={t("loading")} /></Card></>;
  if (query.isError) return <>{back}<p role="alert" className="text-destructive">{query.error.message}</p></>;
  const detail = query.data;
  const busy = validate.isPending || commit.isPending || remove.isPending || confirm.isPending;
  const ready = detail.rowCounts.valid ?? 0;
  const staged = detail.state !== "failed" && detail.state !== "uploading";
  return <>
    <div className="flex flex-col gap-1.5">
      {back}
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="font-mono text-base font-medium break-all">{detail.filename}</h2>
            <StateBadge state={detail.state} />
            {detail.reviewStatus === "needs_review" && <Badge variant="warning">{all("review")}</Badge>}
          </div>
          <p className="text-xs text-muted-foreground">
            {detail.scheduledPeriod ?
              t.rich("scheduled", { month: monthName(detail.scheduledPeriod), account: detail.account.name, created: detail.createdAt.slice(0, 10), date: mono }) :
              t.rich("uploaded", { account: detail.account.name, created: detail.createdAt.slice(0, 10), date: mono })}
            {detail.committedAt && t.rich("committedOn", { committed: detail.committedAt.slice(0, 10), date: mono })}.
          </p>
        </div>
        {staged && detail.state !== "committed" && <div className="flex flex-wrap items-center gap-2">
          {detail.state === "ready" && <Button size="sm" variant="outline" disabled={busy} onClick={() => validate.mutate()}>
            {validate.isPending ? t("checking") : t("checkAgain")}
          </Button>}
          {detail.state === "ready" ? <Button size="sm" disabled={busy || (ready === 0 && detail.conflictCount === 0)} onClick={() => commit.mutate()}>
            {commit.isPending ? t("importing") : t("import", { count: ready })}
          </Button> : detail.mapping && <Button size="sm" disabled={busy} onClick={() => validate.mutate()}>
            {validate.isPending ? t("checking") : t("check")}
          </Button>}
        </div>}
      </div>
    </div>
    <p aria-live="polite" role={message.error ? "alert" : undefined}
      className={cn("text-xs empty:hidden", message.error ? "text-destructive" : "text-muted-foreground")}>{message.text}</p>
    {detail.state === "failed" && <Alert variant="destructive" className={alertClass}>
      <AlertTitle>{t("failed")}</AlertTitle>
      <AlertDescription className={alertDescriptionClass}>{t("failedBody", { message: detail.validationErrors[0]?.message ?? t("failedFallback") })}</AlertDescription>
    </Alert>}
    {detail.state === "uploading" && <Alert className={alertClass}>
      <AlertTitle>{t("stillUploading")}</AlertTitle>
      <AlertDescription className={alertDescriptionClass}>
        <p>{t("stillUploadingBody")}</p>
        <Button size="xs" variant="outline" className="mt-1" onClick={refresh}>{t("refresh")}</Button>
      </AlertDescription>
    </Alert>}
    {staged && <Summary detail={detail} />}
    {detail.state === "needs_attention" && detail.unknownActions.length > 0 && <Alert variant="destructive" className={alertClass}>
      <AlertTitle>{t("unknownActions")}</AlertTitle>
      <AlertDescription className={alertDescriptionClass}>
        <p>{t("unknownActionsBody")}</p>
        <ul className="list-disc pl-5">{detail.unknownActions.map((item) =>
          <li key={item.value}>{t("unknownAction", { value: item.value, rows: item.rows })}</li>)}</ul>
      </AlertDescription>
    </Alert>}
    {detail.state === "needs_attention" && detail.validationErrors.length > 0 && <Alert variant="destructive" className={alertClass}>
      <AlertTitle>{t("errors", { count: detail.errorCount })}</AlertTitle>
      <AlertDescription className={alertDescriptionClass}>
        <p>{t("errorsBody")}</p>
        <ul className="max-h-48 list-disc overflow-y-auto pl-5">{detail.validationErrors.map((item, index) =>
          <li key={index}>{item.row > 0 ? t("rowError", { row: item.row, message: item.message }) : item.message}</li>)}</ul>
      </AlertDescription>
    </Alert>}
    {detail.state === "committed" && <Alert className={alertClass}>
      <AlertTitle>{detail.reviewStatus === "needs_review" ? t("committedReview") : t("committed")}</AlertTitle>
      <AlertDescription className={cn(alertDescriptionClass, "flex flex-col gap-2")}>
        <p>{committedSummary(t, detail)}</p>
        <div className="flex flex-wrap items-center gap-3">
          {detail.reviewStatus === "needs_review" && <Button size="xs" disabled={busy} onClick={() => confirm.mutate(id)}>
            {confirm.isPending ? t("confirming") : t("confirm")}
          </Button>}
          {detail.insertedCount > 0 && <Link className={linkClass} href={`/transactions/?batch=${detail.id}`}>
            {detail.reviewStatus === "needs_review" ? t("reviewTransactions") : t("showTransactions")}
          </Link>}
        </div>
      </AlertDescription>
    </Alert>}
    {staged && detail.state !== "committed" && <MappingEditor
      key={`${detail.mappingVersion ?? "none"}:${detail.unknownActions.map((item) => item.value).join("\u0000")}`}
      detail={detail} onSaved={(next) => { changed(next); void client.invalidateQueries({ queryKey: ["import-rows", id] }); }} report={report} />}
    {staged && <Rows detail={detail} onChanged={changed} report={report} />}
    {detail.state !== "committed" && <div className="flex flex-wrap items-center gap-2">
      {confirmDelete ? <>
        <span className="text-xs">{t("deletePrompt")}</span>
        <Button variant="destructive" size="xs" disabled={busy} onClick={() => remove.mutate()}>{remove.isPending ? t("deleting") : t("delete")}</Button>
        <Button variant="ghost" size="xs" onClick={() => setConfirmDelete(false)}>{t("keep")}</Button>
      </> : <Button variant="ghost" size="xs" className="text-muted-foreground" onClick={() => setConfirmDelete(true)}>
        <Trash2 data-icon="inline-start" aria-hidden="true" />{t("delete")}
      </Button>}
    </div>}
  </>;
}

export function Imports() {
  const batch = useSearchParams().get("batch");
  const id = batch && /^\d+$/.test(batch) ? Number(batch) : null;
  if (id !== null) return <Review key={id} id={id} />;
  return <>
    <div className="grid items-start gap-3 xl:grid-cols-2">
      <Upload />
      <ScheduledImports />
    </div>
    <History />
  </>;
}
