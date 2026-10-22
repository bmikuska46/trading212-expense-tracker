"use client";

import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { tableFeatures, useTable, type ColumnDef } from "@tanstack/react-table";
import { useMemo, useState } from "react";
import { useTranslations } from "next-intl";
import { Controller, useForm } from "react-hook-form";
import { createAccount, listAccounts, listCurrencies, type Account } from "@/lib/api";
import { formatEURCents, formatMoney, formatNumber } from "@/lib/format";
import { showServerErrors } from "@/lib/forms";
import { cn } from "@/lib/utils";
import { cellClass, PanelHeader } from "@/components/panel";
import { SelectCombobox } from "@/components/select-combobox";
import { SortHead, useSort } from "@/components/sortable";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCaption, TableCell, TableFooter, TableHead, TableHeader, TableRow } from "@/components/ui/table";

const features = tableFeatures({});
type T = ReturnType<typeof useTranslations<"accounts">>;

const columns = (t: T): ColumnDef<typeof features, Account>[] => [
  {
    accessorKey: "name", header: t("columns.name"),
    cell: ({ row }) => <Link className="font-medium underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
      href={`/transactions/?account=${row.original.id}`}>{row.original.name}</Link>,
  },
  {
    accessorKey: "provider", header: t("columns.provider"),
    cell: ({ row }) => row.original.provider === "trading212" ? <Badge variant="outline">Trading 212</Badge> : <Badge variant="secondary">{t("manual")}</Badge>,
  },
  { accessorKey: "transactionCount", header: t("columns.transactionCount"), cell: ({ row }) => <span className="font-mono text-muted-foreground">{formatNumber(row.original.transactionCount)}</span> },
  {
    accessorKey: "balance", header: t("columns.balance"),
    cell: ({ row }) => <span className="font-mono">{formatMoney(row.original.balance, row.original.currency)}</span>,
  },
  {
    accessorKey: "balanceEurCents", header: t("columns.balanceEurCents"),
    cell: ({ row }) => <span className="font-mono font-medium">{formatEURCents(row.original.balanceEurCents)}</span>,
  },
];

// Numbers sit right-aligned so the columns line up; the source badge and the
// row count give way first on narrow screens.
const columnClass: Record<string, string> = {
  name: "min-w-32",
  provider: "w-px max-sm:hidden",
  transactionCount: "w-px text-right max-md:hidden",
  balance: "w-px text-right whitespace-nowrap",
  balanceEurCents: "w-px text-right whitespace-nowrap",
};

type Values = { name: string; currency: string };

function CreateAccount() {
  const t = useTranslations("accounts.add");
  const client = useQueryClient();
  const currencies = useQuery({ queryKey: ["currencies"], queryFn: listCurrencies, staleTime: Infinity });
  const [status, setStatus] = useState("");
  const form = useForm<Values>({ defaultValues: { name: "", currency: "EUR" } });
  const { errors } = form.formState;
  const mutation = useMutation({
    mutationFn: createAccount,
    onSuccess: (account) => {
      void client.invalidateQueries({ queryKey: ["accounts"] });
      void client.invalidateQueries({ queryKey: ["dashboard"] });
      form.reset({ name: "", currency: account.currency });
      setStatus(t("added", { name: account.name }));
    },
    onError: (error) => setStatus(showServerErrors(error, form.setError, ["name", "currency"])),
  });
  return <Card size="sm" className="gap-0 self-start py-0">
    <PanelHeader id="add-account-heading" title={t("title")} />
    <CardContent className="px-3.5 py-3">
      <form noValidate aria-labelledby="add-account-heading" className="flex flex-col gap-3"
        onSubmit={form.handleSubmit((values) => { setStatus(""); mutation.mutate(values); })}>
        <div className="flex items-start gap-2">
          <Field data-invalid={Boolean(errors.name)} className="flex-1 gap-1">
            <FieldLabel htmlFor="account-name" className="text-xs text-muted-foreground">{t("name")}</FieldLabel>
            <Input id="account-name" maxLength={100} autoComplete="off" placeholder={t("placeholder")} className="h-7 text-[0.8125rem]"
              aria-invalid={Boolean(errors.name)} aria-describedby={errors.name ? "account-name-error" : undefined}
              {...form.register("name", { required: t("nameRequired"), validate: (value) => value.trim() !== "" || t("nameRequired") })} />
            {errors.name && <FieldError id="account-name-error">{errors.name.message}</FieldError>}
          </Field>
          <Field data-invalid={Boolean(errors.currency)} className="w-24 gap-1">
            <FieldLabel htmlFor="account-currency" className="text-xs text-muted-foreground">{t("currency")}</FieldLabel>
            <Controller control={form.control} name="currency" render={({ field }) =>
              <SelectCombobox id="account-currency" size="sm" className="w-full font-mono" aria-invalid={Boolean(errors.currency)}
                aria-describedby={errors.currency ? "account-currency-error" : undefined} {...field}
                options={(currencies.data ?? ["EUR"]).map((code) => ({ value: code, label: code }))} />} />
            {errors.currency && <FieldError id="account-currency-error">{errors.currency.message}</FieldError>}
          </Field>
        </div>
        <FieldDescription className="text-xs">{t("hint")}</FieldDescription>
        <div className="flex items-center gap-3">
          <Button type="submit" size="sm" disabled={mutation.isPending}>{mutation.isPending ? t("adding") : t("submit")}</Button>
          <p aria-live="polite" className="min-w-0 truncate text-xs text-muted-foreground">{status}</p>
        </div>
      </form>
    </CardContent>
  </Card>;
}

const sortKeys = {
  name: (account: Account) => account.name,
  provider: (account: Account) => account.provider === "trading212" ? "Trading 212" : "Manual",
  transactionCount: (account: Account) => account.transactionCount,
  balance: (account: Account) => Number(account.balance),
  balanceEurCents: (account: Account) => account.balanceEurCents,
};

function AccountTable({ data }: { data: Account[] }) {
  const t = useTranslations("accounts");
  const { rows, sort, toggle } = useSort(data, sortKeys, { key: "name", desc: false });
  const columnDefs = useMemo(() => columns(t), [t]);
  const table = useTable({ features, columns: columnDefs, data: rows });
  const totalEurCents = data.reduce((sum, account) => sum + account.balanceEurCents, 0);
  const totalRows = data.reduce((sum, account) => sum + account.transactionCount, 0);
  return <Table>
    <TableCaption className="sr-only">{t("caption")}</TableCaption>
    <TableHeader>{table.getHeaderGroups().map((group) => <TableRow key={group.id} className="hover:bg-transparent">
      {group.headers.map((header) => <SortHead key={header.id} sort={sort} sortKey={header.column.id as keyof typeof sortKeys} onSort={toggle}
        className={columnClass[header.column.id]}>
        <table.FlexRender header={header} />
      </SortHead>)}
    </TableRow>)}</TableHeader>
    <TableBody>{table.getRowModel().rows.map((row) => <TableRow key={row.id} className="hover:bg-muted/40">
      {row.getAllCells().map((cell) => <TableCell key={cell.id} className={cn(cellClass, columnClass[cell.column.id])}>
        <table.FlexRender cell={cell} />
      </TableCell>)}
    </TableRow>)}</TableBody>
    <TableFooter className="bg-muted/60">
      <TableRow className="hover:bg-transparent">
        <TableHead scope="row" className={cn(cellClass, "h-auto text-xs font-medium text-muted-foreground")}>{t("total")}</TableHead>
        <TableCell className={cn(cellClass, columnClass.provider)} />
        <TableCell className={cn(cellClass, "font-mono text-muted-foreground", columnClass.transactionCount)}>{formatNumber(totalRows)}</TableCell>
        <TableCell className={cn(cellClass, columnClass.balance)} />
        <TableCell className={cn(cellClass, "font-mono font-medium", columnClass.balanceEurCents)}>{formatEURCents(totalEurCents)}</TableCell>
      </TableRow>
    </TableFooter>
  </Table>;
}

export function Accounts() {
  const t = useTranslations("accounts");
  const query = useQuery({ queryKey: ["accounts"], queryFn: listAccounts });
  return <div className="grid items-start gap-3 lg:grid-cols-[minmax(0,1fr)_20rem]">
    <Card size="sm" className="gap-0 py-0">
      <PanelHeader id="accounts-heading" title={t("title")} count={query.data?.length} />
      {query.isPending ? <div role="status" aria-label={t("loading")} className="flex flex-col gap-2 p-3.5">
        {Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-6" />)}
      </div> :
        query.isError ? <p role="alert" className="px-3.5 py-3 text-sm text-destructive">{query.error.message}</p> :
        query.data.length === 0 ? <Empty className="py-10">
          <EmptyHeader>
            <EmptyTitle>{t("empty.title")}</EmptyTitle>
            <EmptyDescription>{t("empty.body")}</EmptyDescription>
          </EmptyHeader>
        </Empty> :
        <AccountTable data={query.data} />}
    </Card>
    <CreateAccount />
  </div>;
}
