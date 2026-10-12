"use client";

import Link from "next/link";
import { useDeferredValue, useState } from "react";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { tableFeatures, useTable, type ColumnDef } from "@tanstack/react-table";
import { useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { ChevronLeft, ChevronRight, Plus, Search } from "lucide-react";
import {
  createEntity, deleteRule, listCategories, listPayees, listRules, mergeEntity, renameEntity, saveRule, updateRule,
  ledgerQueries, type Entity, type EntityKind, type PayeeSort, type Rule,
} from "@/lib/api";
import { formatNumber } from "@/lib/format";
import { showServerErrors } from "@/lib/forms";
import { cn } from "@/lib/utils";
import { cellClass, footerClass, headClass, PanelHeader } from "@/components/panel";
import { nextSort, SortHead, useSort, type Sort } from "@/components/sortable";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import { Card, CardFooter } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput } from "@/components/ui/input-group";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type T = ReturnType<typeof useTranslations<"catalog">>;

function useRefresh() {
  const client = useQueryClient();
  return () => {
    for (const key of ["rules", ...ledgerQueries]) void client.invalidateQueries({ queryKey: [key] });
  };
}

function CreateEntity({ kind }: { kind: EntityKind }) {
  const t = useTranslations("catalog");
  const refresh = useRefresh();
  const [status, setStatus] = useState("");
  const form = useForm<{ name: string }>({ defaultValues: { name: "" } });
  const mutation = useMutation({
    mutationFn: (name: string) => createEntity(kind, name),
    onSuccess: (entity, name) => {
      refresh();
      form.reset();
      setStatus(entity.name.toLowerCase() === name.trim().toLowerCase() || entity.transactionCount > 0
        ? t("added", { name: entity.name }) : t(`kinds.${kind}.matches`, { input: name.trim(), name: entity.name }));
    },
    onError: (error) => setStatus(showServerErrors(error, form.setError, ["name"])),
  });
  const error = form.formState.errors.name;
  // One field with the submit button inside it, so adding stays a single
  // line in the panel toolbar.
  return <form noValidate className="flex min-w-0 flex-col gap-1 max-sm:w-full"
    onSubmit={form.handleSubmit(({ name }) => { setStatus(""); mutation.mutate(name); })}>
    <Field data-invalid={Boolean(error)} className="gap-1 sm:w-64">
      <FieldLabel htmlFor={`new-${kind}`} className="sr-only">{t(`kinds.${kind}.new`)}</FieldLabel>
      <InputGroup className="h-7">
        <InputGroupAddon><Plus aria-hidden="true" /></InputGroupAddon>
        <InputGroupInput id={`new-${kind}`} maxLength={100} autoComplete="off" placeholder={t(`kinds.${kind}.new`)} aria-invalid={Boolean(error)}
          {...form.register("name", { required: t("nameRequired"), validate: (value) => value.trim() !== "" || t("nameRequired") })} />
        <InputGroupAddon align="inline-end">
          <InputGroupButton type="submit" size="xs" variant="secondary" className="h-5" disabled={mutation.isPending}>{mutation.isPending ? t("adding") : t("add")}</InputGroupButton>
        </InputGroupAddon>
      </InputGroup>
      {error && <FieldError className="text-xs">{error.message}</FieldError>}
    </Field>
    <p aria-live="polite" className="text-xs text-muted-foreground empty:hidden">{status}</p>
  </form>;
}

// MergeTarget picks the entry that keeps its name. Payees are searched on the
// server because the list can be long.
function MergeTarget({ kind, entity, value, onChange }: { kind: EntityKind; entity: Entity; value: string; onChange: (value: string) => void }) {
  const t = useTranslations("catalog");
  const [search, setSearch] = useState("");
  const deferred = useDeferredValue(search.trim());
  const categories = useQuery({ queryKey: ["categories"], queryFn: listCategories, enabled: kind === "categories" });
  const payees = useQuery({
    queryKey: ["payees", { q: deferred, pageSize: 50 }],
    queryFn: ({ signal }) => listPayees({ q: deferred, pageSize: 50 }, { signal }),
    enabled: kind === "payees",
    placeholderData: keepPreviousData,
  });
  const options = (kind === "categories" ? categories.data : payees.data?.payees)?.filter((item) => item.id !== entity.id) ?? [];
  return <div className="flex flex-col gap-3">
    {kind === "payees" && <Field>
      <FieldLabel htmlFor="merge-search">{t("findPayee")}</FieldLabel>
      <Input id="merge-search" type="search" value={search} maxLength={100} onChange={(event) => setSearch(event.target.value)} autoComplete="off" />
    </Field>}
    <Field>
      <FieldLabel htmlFor="merge-target">{t("mergeInto")}</FieldLabel>
      <NativeSelect id="merge-target" className="w-full" value={value} onChange={(event) => onChange(event.target.value)}>
        <NativeSelectOption value="">{t(`kinds.${kind}.choose`)}</NativeSelectOption>
        {options.map((item) => <NativeSelectOption key={item.id} value={item.id}>{item.name}</NativeSelectOption>)}
      </NativeSelect>
    </Field>
  </div>;
}

function EntityDialog({ kind, entity, onClose }: { kind: EntityKind; entity: Entity | null; onClose: () => void }) {
  const t = useTranslations("catalog.dialog");
  return <Dialog open={entity !== null} onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{t("title", { name: entity?.name ?? "" })}</DialogTitle>
        <DialogDescription>{t("description")}</DialogDescription>
      </DialogHeader>
      {entity && <EntityForms key={entity.id} kind={kind} entity={entity} onClose={onClose} />}
    </DialogContent>
  </Dialog>;
}

function EntityForms({ kind, entity, onClose }: { kind: EntityKind; entity: Entity; onClose: () => void }) {
  const t = useTranslations("catalog");
  const refresh = useRefresh();
  const [status, setStatus] = useState("");
  const [target, setTarget] = useState("");
  const form = useForm<{ name: string }>({ defaultValues: { name: entity.name } });
  const rename = useMutation({
    mutationFn: (name: string) => renameEntity(kind, entity.id, name),
    onSuccess: () => { refresh(); onClose(); },
    onError: (error) => setStatus(showServerErrors(error, form.setError, ["name"])),
  });
  const merge = useMutation({
    mutationFn: () => mergeEntity(kind, entity.id, Number(target)),
    onSuccess: () => { refresh(); onClose(); },
    onError: (error) => setStatus(error.message),
  });
  const error = form.formState.errors.name;
  return <div className="flex flex-col gap-5">
    {entity.aliases.length > 0 && <p className="text-muted-foreground">{t("alsoMatches", { aliases: entity.aliases.join(", ") })}</p>}
    <form noValidate className="flex flex-col gap-3" onSubmit={form.handleSubmit(({ name }) => { setStatus(""); rename.mutate(name); })}>
      <Field data-invalid={Boolean(error)}>
        <FieldLabel htmlFor="rename-name">{t("name")}</FieldLabel>
        <Input id="rename-name" maxLength={100} autoComplete="off" aria-invalid={Boolean(error)} aria-describedby="rename-description"
          {...form.register("name", { required: t("nameRequired") })} />
        <FieldDescription id="rename-description">{t("renameHint")}</FieldDescription>
        {error && <FieldError>{error.message}</FieldError>}
      </Field>
      <Button type="submit" className="w-fit" disabled={rename.isPending}>{rename.isPending ? t("renaming") : t("rename")}</Button>
    </form>
    {!entity.system && <>
      <Separator />
      <form className="flex flex-col gap-3" onSubmit={(event) => { event.preventDefault(); setStatus(""); if (target) merge.mutate(); }}>
        <MergeTarget kind={kind} entity={entity} value={target} onChange={setTarget} />
        <p className="text-muted-foreground">
          {t(`kinds.${kind}.move`, { count: entity.transactionCount })}
          {kind === "payees" && ` ${t("keepRule")}`}
        </p>
        <Button type="submit" variant="outline" className="w-fit" disabled={!target || merge.isPending}>{merge.isPending ? t("merging") : t("merge")}</Button>
      </form>
    </>}
    <p role={status ? "alert" : undefined} className="text-destructive empty:hidden">{status}</p>
  </div>;
}

const features = tableFeatures({});

// chip outlines a category name the same way the register does.
const chip = "rounded-[5px] border border-input px-1.5 py-px text-xs whitespace-nowrap text-muted-foreground";

function entityColumns(t: T, kind: EntityKind, change: (entity: Entity) => void): ColumnDef<typeof features, Entity>[] {
  return [
    {
      accessorKey: "name", header: t(`kinds.${kind}.column`),
      cell: ({ row }) => <div className="flex min-w-0 flex-col gap-0.5">
        <span className="flex items-center gap-2">
          <button type="button" onClick={() => change(row.original)}
            className="w-fit text-left font-medium underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring">
            {row.original.name}<span className="sr-only">{t("columns.renameOrMerge")}</span>
          </button>
          {row.original.system && <Badge variant="secondary">{t("columns.fallback")}</Badge>}
        </span>
        {row.original.aliases.length > 0 && <span className="text-xs text-faint">{t("columns.also", { aliases: row.original.aliases.join(", ") })}</span>}
      </div>,
    },
    ...(kind === "payees" ? [{
      id: "rule", header: t("columns.rule"),
      cell: ({ row }: { row: { original: Entity } }) => row.original.ruleCategoryName
        ? <span className={chip}>{row.original.ruleCategoryName}</span> : <span className="text-xs text-faint">{t("columns.none")}</span>,
    }] : []),
    {
      accessorKey: "transactionCount", header: t("columns.rows"),
      cell: ({ row }) => <Link className="font-mono text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
        href={`/transactions/?${kind === "payees" ? "payee" : "category"}=${row.original.id}`}>
        {formatNumber(row.original.transactionCount)}<span className="sr-only"> {t("columns.transactions", { count: row.original.transactionCount })}</span>
      </Link>,
    },
  ];
}

const columnClass: Record<string, string> = { rule: "w-px max-sm:hidden", transactionCount: "w-px text-right" };

// The rows arrive sorted; Categories sorts in the browser and Payees on the
// server, since payees are paged.
function EntityTable({ kind, rows, caption, change, sort, onSort }: {
  kind: EntityKind; rows: Entity[]; caption: string; change: (entity: Entity) => void; sort: Sort<PayeeSort>; onSort: (key: PayeeSort) => void;
}) {
  const t = useTranslations("catalog");
  const table = useTable({ features, columns: entityColumns(t, kind, change), data: rows, getRowId: (row) => String(row.id) });
  return <Table>
    <TableCaption className="sr-only">{caption}</TableCaption>
    <TableHeader>{table.getHeaderGroups().map((group) => <TableRow key={group.id} className="hover:bg-transparent">
      {group.headers.map((header) => <SortHead key={header.id} sort={sort} sortKey={header.column.id as PayeeSort} onSort={onSort}
        className={columnClass[header.column.id]}>
        <table.FlexRender header={header} />
      </SortHead>)}
    </TableRow>)}</TableHeader>
    <TableBody>{table.getRowModel().rows.map((row) => <TableRow key={row.id} className="hover:bg-muted/40">
      {row.getAllCells().map((cell) => <TableCell key={cell.id} className={cn(cellClass, "whitespace-normal", columnClass[cell.column.id])}>
        <table.FlexRender cell={cell} />
      </TableCell>)}
    </TableRow>)}</TableBody>
  </Table>;
}

function Loading({ label }: { label: string }) {
  return <div role="status" aria-label={label} className="flex flex-col gap-2 p-3.5">
    {Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-6" />)}
  </div>;
}

function RuleRow({ rule, categories }: { rule: Rule; categories: Entity[] }) {
  const t = useTranslations("catalog.rules");
  const refresh = useRefresh();
  const update = useMutation({ mutationFn: (input: { categoryId: number; enabled: boolean }) => updateRule(rule.id, input), onSuccess: refresh });
  const remove = useMutation({ mutationFn: () => deleteRule(rule.id), onSuccess: refresh });
  return <TableRow className="hover:bg-muted/40">
    <TableCell className={cn(cellClass, "font-medium whitespace-normal")}>{rule.payeeName}</TableCell>
    <TableCell className={cellClass}>
      <label htmlFor={`rule-${rule.id}-category`} className="sr-only">{t("categoryFor", { payee: rule.payeeName })}</label>
      <NativeSelect id={`rule-${rule.id}-category`} size="sm" className="max-w-40" value={String(rule.categoryId)} disabled={update.isPending}
        onChange={(event) => update.mutate({ categoryId: Number(event.target.value), enabled: rule.enabled })}>
        {categories.map((category) => <NativeSelectOption key={category.id} value={category.id}>{category.name}</NativeSelectOption>)}
      </NativeSelect>
    </TableCell>
    <TableCell className={cellClass}>
      <div className="flex items-center gap-2">
        <Checkbox id={`rule-${rule.id}-enabled`} checked={rule.enabled} disabled={update.isPending}
          onCheckedChange={(value) => update.mutate({ categoryId: rule.categoryId, enabled: value === true })} />
        <label htmlFor={`rule-${rule.id}-enabled`} className={cn("font-mono text-xs", rule.enabled ? "text-foreground" : "text-faint")}>{rule.enabled ? t("on") : t("off")}</label>
      </div>
    </TableCell>
    <TableCell className={cn(cellClass, "w-px text-right")}>
      <Button size="xs" variant="ghost" disabled={remove.isPending} onClick={() => remove.mutate()}>{t("delete")}<span className="sr-only">{t("deleteFor", { payee: rule.payeeName })}</span></Button>
      {(update.isError || remove.isError) && <p role="alert" className="text-xs text-destructive">{(update.error ?? remove.error)?.message}</p>}
    </TableCell>
  </TableRow>;
}

const ruleKeys = {
  payee: (rule: Rule) => rule.payeeName,
  category: (rule: Rule) => rule.categoryName,
  enabled: (rule: Rule) => Number(rule.enabled),
};

function Rules({ categories }: { categories: Entity[] }) {
  const t = useTranslations("catalog.rules");
  const common = useTranslations("common");
  const refresh = useRefresh();
  const rules = useQuery({ queryKey: ["rules"], queryFn: listRules });
  const sorted = useSort(rules.data ?? [], ruleKeys, { key: "payee", desc: false });
  const head = { sort: sorted.sort, onSort: sorted.toggle };
  const [status, setStatus] = useState("");
  const form = useForm<{ payee: string; category: string }>({ defaultValues: { payee: "", category: "" } });
  const create = useMutation({
    mutationFn: saveRule,
    onSuccess: () => { refresh(); form.reset(); setStatus(t("saved")); },
    onError: (error) => setStatus(showServerErrors(error, form.setError, ["payee", "category"])),
  });
  const { errors } = form.formState;
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="rules-heading" title={t("title")} count={rules.data?.length} />
    <form noValidate aria-labelledby="rules-heading" className="flex flex-wrap items-start gap-2 border-b px-3.5 py-2.5"
      onSubmit={form.handleSubmit((values) => { setStatus(""); create.mutate({ payee: values.payee, category: values.category }); })}>
      <Field data-invalid={Boolean(errors.payee)} className="w-full gap-1 sm:w-44">
        <FieldLabel htmlFor="rule-payee" className="sr-only">{t("payee")}</FieldLabel>
        <Input id="rule-payee" maxLength={100} autoComplete="off" placeholder={t("payee")} className="h-7 text-[0.8125rem]"
          aria-invalid={Boolean(errors.payee)} {...form.register("payee", { required: t("payeeRequired") })} />
        {errors.payee && <FieldError className="text-xs">{errors.payee.message}</FieldError>}
      </Field>
      <Field data-invalid={Boolean(errors.category)} className="w-full gap-1 sm:w-44">
        <FieldLabel htmlFor="rule-category" className="sr-only">{t("category")}</FieldLabel>
        <Input id="rule-category" maxLength={100} autoComplete="off" list="rule-category-options" placeholder={t("category")} className="h-7 text-[0.8125rem]"
          aria-invalid={Boolean(errors.category)} {...form.register("category", { required: t("categoryRequired") })} />
        <datalist id="rule-category-options">{categories.map((category) => <option key={category.id} value={category.name} />)}</datalist>
        {errors.category && <FieldError className="text-xs">{errors.category.message}</FieldError>}
      </Field>
      <Button type="submit" size="sm" variant="secondary" disabled={create.isPending}>{create.isPending ? common("saving") : t("save")}</Button>
      <p aria-live="polite" className="basis-full text-xs text-muted-foreground empty:hidden">{status}</p>
    </form>
    {rules.isPending ? <Loading label={t("loading")} /> :
      rules.isError ? <p role="alert" className="px-3.5 py-3 text-sm text-destructive">{rules.error.message}</p> :
      rules.data.length === 0 ? <p className="px-3.5 py-6 text-center text-xs text-muted-foreground">{t("empty")}</p> :
      <Table>
        <TableCaption className="sr-only">{t("caption")}</TableCaption>
        <TableHeader><TableRow className="hover:bg-transparent">
          <SortHead {...head} sortKey="payee">{t("payee")}</SortHead><SortHead {...head} sortKey="category">{t("category")}</SortHead>
          <SortHead {...head} sortKey="enabled">{t("active")}</SortHead><TableHead scope="col" className={headClass}><span className="sr-only">{common("actions")}</span></TableHead>
        </TableRow></TableHeader>
        <TableBody>{sorted.rows.map((rule) => <RuleRow key={rule.id} rule={rule} categories={categories} />)}</TableBody>
      </Table>}
    <CardFooter className={footerClass}>
      {t("footer")}
    </CardFooter>
  </Card>;
}

const categoryKeys = {
  name: (category: Entity) => category.name,
  rule: () => null,
  transactionCount: (category: Entity) => category.transactionCount,
};

export function Categories() {
  const t = useTranslations("catalog.categories");
  const categories = useQuery({ queryKey: ["categories"], queryFn: listCategories });
  const sorted = useSort(categories.data ?? [], categoryKeys, { key: "name", desc: false });
  const [changing, setChanging] = useState<Entity | null>(null);
  if (categories.isPending) return <Card size="sm" className="py-0"><Loading label={t("loading")} /></Card>;
  if (categories.isError) return <p role="alert" className="text-destructive">{categories.error.message}</p>;
  return <div className="grid items-start gap-3 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
    <Card size="sm" className="gap-0 py-0">
      <PanelHeader title={t("title")} count={categories.data.length} />
      <div className="border-b px-3.5 py-2.5"><CreateEntity kind="categories" /></div>
      <EntityTable kind="categories" rows={sorted.rows} change={setChanging} sort={sorted.sort} onSort={sorted.toggle}
        caption={t("caption")} />
      <CardFooter className={footerClass}>
        {t("footer")}
      </CardFooter>
    </Card>
    <Rules categories={categories.data} />
    <EntityDialog kind="categories" entity={changing} onClose={() => setChanging(null)} />
  </div>;
}

export function Payees() {
  const t = useTranslations("catalog.payees");
  const common = useTranslations("common");
  const [search, setSearch] = useState("");
  const [page, setPage] = useState(1);
  const [sort, setSort] = useState<Sort<PayeeSort>>({ key: "name", desc: false });
  const deferred = useDeferredValue(search.trim());
  const pageSize = 50;
  const query = { q: deferred, page, pageSize, sort: sort.key, dir: sort.desc ? "desc" as const : "asc" as const };
  const payees = useQuery({
    queryKey: ["payees", query],
    queryFn: ({ signal }) => listPayees(query, { signal }),
    placeholderData: keepPreviousData,
  });
  const [changing, setChanging] = useState<Entity | null>(null);
  const pages = payees.data ? Math.max(1, Math.ceil(payees.data.total / pageSize)) : 1;
  const total = payees.data?.total ?? 0;
  const first = total === 0 ? 0 : (page - 1) * pageSize + 1;
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader title={t("title")} count={payees.data ? formatNumber(total) : undefined} />
    <div className="flex flex-wrap items-start justify-between gap-2 border-b px-3.5 py-2.5">
      <Field className="w-full gap-1 sm:w-64">
        <FieldLabel htmlFor="payee-search" className="sr-only">{t("search")}</FieldLabel>
        <InputGroup className="h-7">
          <InputGroupAddon><Search aria-hidden="true" /></InputGroupAddon>
          <InputGroupInput id="payee-search" type="search" maxLength={100} value={search} autoComplete="off" placeholder={t("search")}
            onChange={(event) => { setSearch(event.target.value); setPage(1); }} />
        </InputGroup>
      </Field>
      <CreateEntity kind="payees" />
    </div>
    <div aria-busy={payees.isPlaceholderData} className={cn("transition-opacity", payees.isPlaceholderData && "opacity-60")}>
      {payees.isPending ? <Loading label={t("loading")} /> :
        payees.isError ? <p role="alert" className="px-3.5 py-3 text-sm text-destructive">{payees.error.message}</p> :
        total === 0 ? <p className="px-3.5 py-10 text-center text-xs text-muted-foreground">{deferred ? t("noMatch") : t("none")}</p> :
        <EntityTable kind="payees" rows={payees.data.payees} change={setChanging} sort={sort}
          onSort={(key) => { setSort(nextSort(sort, key, key === "transactionCount")); setPage(1); }}
          caption={t("caption", { count: total })} />}
    </div>
    <CardFooter className="flex-wrap justify-between gap-3 border-t bg-transparent px-3.5 py-2 text-xs text-faint">
      <span>
        {total > 0 && <span className="mr-3 font-mono">{t("range", { first: formatNumber(first), last: formatNumber(Math.min(page * pageSize, total)), total: formatNumber(total) })}</span>}
        {t.rich("rulesHint", { link: (chunks) => <Link href="/categories/" className="underline underline-offset-4 hover:text-foreground">{chunks}</Link> })}
      </span>
      {pages > 1 && <nav aria-label={t("pages")} className="flex items-center gap-3">
        <span className="font-mono">{page}/{pages}</span>
        <ButtonGroup>
          <Button size="icon-sm" variant="outline" aria-label={common("pagination.previous")} disabled={page <= 1} onClick={() => setPage(page - 1)}>
            <ChevronLeft aria-hidden="true" />
          </Button>
          <Button size="icon-sm" variant="outline" aria-label={common("pagination.next")} disabled={page >= pages} onClick={() => setPage(page + 1)}>
            <ChevronRight aria-hidden="true" />
          </Button>
        </ButtonGroup>
      </nav>}
    </CardFooter>
    <EntityDialog kind="payees" entity={changing} onClose={() => setChanging(null)} />
  </Card>;
}
