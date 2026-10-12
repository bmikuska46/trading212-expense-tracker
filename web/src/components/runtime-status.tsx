"use client";

import { useQuery } from "@tanstack/react-query";
import { useHotkey } from "@tanstack/react-hotkeys";
import { tableFeatures, useTable, type ColumnDef } from "@tanstack/react-table";
import { useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { useMemo, useState } from "react";
import { RefreshCw, Search } from "lucide-react";
import { getRuntime } from "@/lib/api";
import { cn } from "@/lib/utils";
import { cellClass, footerClass, headClass, PanelHeader } from "@/components/panel";
import { Button } from "@/components/ui/button";
import { Card, CardFooter } from "@/components/ui/card";
import { Field, FieldError, FieldLabel } from "@/components/ui/field";
import { InputGroup, InputGroupAddon, InputGroupButton, InputGroupInput } from "@/components/ui/input-group";
import { Kbd, KbdGroup } from "@/components/ui/kbd";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

type RuntimeRow = { name: string; value: string };
const features = tableFeatures({});
const emptyRows: RuntimeRow[] = [];

export function RuntimeStatus() {
  const t = useTranslations("settings.runtime");
  const shell = useTranslations("shell");
  const columns = useMemo<ColumnDef<typeof features, RuntimeRow>[]>(() => [
    { accessorKey: "name", header: t("component"), cell: ({ row }) => <span className="text-muted-foreground">{row.original.name}</span> },
    { accessorKey: "value", header: t("status"), cell: ({ row }) => <span className="font-mono">{row.original.value}</span> },
  ], [t]);
  const query = useQuery({ queryKey: ["runtime"], queryFn: getRuntime });
  const [filter, setFilter] = useState("");
  const form = useForm({ defaultValues: { filter: "" } });
  const filterError = form.formState.errors.filter;
  useHotkey("Shift+R", () => { void query.refetch(); }, { ignoreInputs: true });
  const data = query.data;
  const rows = data ? [
    { name: t("server"), value: t("connected") },
    { name: t("database"), value: shell("encrypted", { version: data.cipherVersion }) },
    { name: t("created"), value: data.createdAt },
  ].filter((row) => `${row.name} ${row.value}`.toLowerCase().includes(filter.toLowerCase())) : emptyRows;
  const table = useTable({ features, columns, data: rows });
  const state = query.isPending ? "checking" : query.isError ? "offline" : "connected";
  return (
    <Card size="sm" className="gap-0 py-0">
      <PanelHeader id="runtime-heading" title={t("title")} action={
        <span aria-live="polite" className="flex items-center gap-1.5 font-mono">
          <span aria-hidden="true" className={cn("size-1.5 rounded-full",
            state === "connected" ? "bg-mark" : state === "offline" ? "bg-destructive" : "bg-faint")} />
          {t(`states.${state}`)}
        </span>} />
      {query.isError && <p role="alert" className="px-3.5 py-3 text-xs text-destructive">{query.error.message}</p>}
      {data && !query.isError && <>
        <form onSubmit={form.handleSubmit(({ filter }) => setFilter(filter.trim()))} className="border-b px-3.5 py-2">
          <Field data-invalid={Boolean(filterError)} className="gap-1">
            <FieldLabel htmlFor="status-filter" className="sr-only">{t("filter")}</FieldLabel>
            <InputGroup className="h-7">
              <InputGroupAddon><Search aria-hidden="true" /></InputGroupAddon>
              <InputGroupInput id="status-filter" type="search" maxLength={80} placeholder={t("filter")} className="text-[0.8125rem]"
                aria-invalid={Boolean(filterError)} aria-describedby={filterError ? "status-filter-error" : undefined}
                {...form.register("filter", { maxLength: { value: 80, message: t("filterMax") } })} />
              <InputGroupAddon align="inline-end">
                <InputGroupButton type="submit" size="xs" variant="secondary" className="h-5">{t("filterButton")}</InputGroupButton>
              </InputGroupAddon>
            </InputGroup>
            {filterError && <FieldError id="status-filter-error" className="text-xs">{filterError.message}</FieldError>}
          </Field>
        </form>
        <Table>
          <TableCaption className="sr-only">{t("caption")}</TableCaption>
          <TableHeader>{table.getHeaderGroups().map((group) => <TableRow key={group.id} className="hover:bg-transparent">
            {group.headers.map((header) => <TableHead key={header.id} scope="col" className={headClass}><table.FlexRender header={header} /></TableHead>)}
          </TableRow>)}</TableHeader>
          <TableBody>{table.getRowModel().rows.length ? table.getRowModel().rows.map((row) => <TableRow key={row.id} className="hover:bg-muted/40">
            {row.getAllCells().map((cell) => <TableCell key={cell.id} className={cn(cellClass, "text-xs whitespace-normal")}><table.FlexRender cell={cell} /></TableCell>)}
          </TableRow>) : <TableRow className="hover:bg-transparent">
            <TableCell colSpan={columns.length} className={cn(cellClass, "text-xs text-muted-foreground")}>{t("noMatches")}</TableCell>
          </TableRow>}</TableBody>
        </Table>
      </>}
      <CardFooter className={cn(footerClass, "justify-between gap-3 py-2")}>
        <span className="flex items-center gap-1.5"><KbdGroup><Kbd>Shift</Kbd><Kbd>R</Kbd></KbdGroup>{t("checksAgain")}</span>
        <Button variant="outline" size="xs" disabled={query.isFetching} onClick={() => { void query.refetch(); }}>
          <RefreshCw data-icon="inline-start" aria-hidden="true" className={cn(query.isFetching && "animate-spin")} />
          {query.isFetching ? t("checking") : t("check")}
        </Button>
      </CardFooter>
    </Card>
  );
}
