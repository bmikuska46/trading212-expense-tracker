"use client";

import Link from "next/link";
import { useDeferredValue, useRef, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Controller, useFieldArray, useForm, useWatch, type Path } from "react-hook-form";
import { Plus, Trash2 } from "lucide-react";
import { useTranslations } from "next-intl";
import {
  createTransaction, deleteTransaction, getTransaction, listPayees, listTransactions, saveRule, updateTransaction,
  ledgerQueries, type Account, type Entity, type Kind, type RuleSuggestion, type TransactionDetail, type TransactionInput,
} from "@/lib/api";
import { formatEURCents, formatMoney, todayIn } from "@/lib/format";
import { showServerErrors } from "@/lib/forms";
import { cn } from "@/lib/utils";
import { alertClass, alertDescriptionClass, compactFields, legendClass } from "@/components/panel";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Skeleton } from "@/components/ui/skeleton";

export const kinds: Kind[] = ["expense", "income", "transfer", "refund"];
export const isKind = (value: string): value is Kind => (kinds as string[]).includes(value);

type SplitValues = { categoryId: string; amount: string; notes: string };
type Values = {
  kind: Kind; accountId: string; toAccountId: string; date: string; amount: string; payee: string;
  categoryId: string; refundOf: string; notes: string; splits: SplitValues[];
};

// Fields match the dense console forms elsewhere.
const inputClass = "h-8 text-[0.8125rem]";
const labelLegend = "mb-1.5 text-muted-foreground data-[variant=label]:text-xs";

// Amounts accept a decimal comma too, as typed in Slovak, and are sent with a point.
const amountPattern = /^\s*\d+([.,]\d{1,2})?\s*$/;
const amountValue = (amount: string) => amount.trim().replace(",", ".");

function magnitude(amount: string): string {
  return amount.startsWith("-") ? amount.slice(1) : amount;
}

// When editing, an empty category means Uncategorized, so that category is
// shown as the empty choice rather than listed twice.
function initialValues(detail: TransactionDetail | undefined, accounts: Account[], account: number | undefined, timezone: string, uncategorized: number | undefined): Values {
  if (detail) {
    return {
      kind: detail.kind, accountId: String(detail.accountId), toAccountId: "", date: detail.date, amount: magnitude(detail.amount),
      payee: detail.payeeName, categoryId: detail.categoryId && (detail.kind === "refund" || detail.categoryId !== uncategorized) ? String(detail.categoryId) : "", refundOf: detail.refundOf ? String(detail.refundOf) : "",
      notes: detail.notes, splits: detail.splits.map((split) => ({ categoryId: String(split.categoryId), amount: magnitude(split.amount), notes: split.notes })),
    };
  }
  const initial = accounts.find((item) => item.id === account) ?? accounts[0];
  return {
    kind: "expense", accountId: initial ? String(initial.id) : "", toAccountId: "", date: todayIn(timezone), amount: "", payee: "",
    categoryId: "", refundOf: "", notes: "", splits: [],
  };
}

function toInput(values: Values, revision: number | undefined): TransactionInput {
  const splitting = (values.kind === "expense" || values.kind === "income") && values.splits.length > 0;
  return {
    revision,
    accountId: Number(values.accountId),
    toAccountId: values.kind === "transfer" && values.toAccountId ? Number(values.toAccountId) : undefined,
    date: values.date,
    kind: values.kind,
    amount: amountValue(values.amount),
    payee: values.kind === "transfer" ? "" : values.payee.trim(),
    categoryId: values.kind === "transfer" || splitting || !values.categoryId ? null : Number(values.categoryId),
    refundOf: values.kind === "refund" && values.refundOf ? Number(values.refundOf) : null,
    notes: values.notes,
    splits: splitting ? values.splits.map((split) => ({ categoryId: Number(split.categoryId), amount: amountValue(split.amount), notes: split.notes })) : [],
  };
}

// PayeeInput suggests existing payees while still accepting a new name, which
// the server creates or matches to an equivalent one.
function PayeeInput({ id, value, onChange, invalid, describedBy }: {
  id: string; value: string; onChange: (value: string) => void; invalid: boolean; describedBy?: string;
}) {
  const search = useDeferredValue(value.trim());
  const payees = useQuery({
    queryKey: ["payees", { q: search, pageSize: 20 }],
    queryFn: ({ signal }) => listPayees({ q: search, pageSize: 20 }, { signal }),
    enabled: search.length > 0,
    staleTime: 30_000,
  });
  return <>
    <Input id={id} className={inputClass} value={value} onChange={(event) => onChange(event.target.value)} list={`${id}-options`} maxLength={100}
      autoComplete="off" aria-invalid={invalid} aria-describedby={describedBy} />
    <datalist id={`${id}-options`}>
      {payees.data?.payees.map((payee) => <option key={payee.id} value={payee.name} />)}
    </datalist>
  </>;
}

// RefundTarget lists recent expenses so a refund can be linked to the
// purchase it reverses. Searching by payee keeps the list short.
function RefundTarget({ value, onChange, payee, current, invalid }: {
  value: string; onChange: (value: string) => void; payee: string; current: TransactionDetail["refundOfTransaction"]; invalid: boolean;
}) {
  const t = useTranslations("editor");
  const search = useDeferredValue(payee.trim());
  const expenses = useQuery({
    queryKey: ["transactions", { kind: "expense", q: search, pageSize: "50" }],
    queryFn: ({ signal }) => listTransactions({ kind: "expense", q: search, pageSize: "50" }, { signal }),
  });
  const options = expenses.data?.transactions ?? [];
  const listed = current && !options.some((item) => item.id === current.id) ? [current, ...options] : options;
  return <Field data-invalid={invalid}>
    <FieldLabel htmlFor="editor-refund">{t("refundOf")}</FieldLabel>
    <NativeSelect id="editor-refund" size="sm" className="w-full" value={value} onChange={(event) => onChange(event.target.value)}
      aria-invalid={invalid} aria-describedby="editor-refund-description">
      <NativeSelectOption value="">{t("notLinked")}</NativeSelectOption>
      {listed.map((expense) => <NativeSelectOption key={expense.id} value={expense.id}>
        {expense.date} - {expense.payeeName || t("noPayee")} - {formatMoney(magnitude(expense.amount), expense.currency)}
      </NativeSelectOption>)}
    </NativeSelect>
    <FieldDescription id="editor-refund-description">
      {t("refundHint")} {search ? t("refundMatching", { search }) : t("refundSearch")}
    </FieldDescription>
  </Field>;
}

// Source details read like the settings panels: a mono label column beside
// the values, inside a hairline box.
function SourceDetails({ id, title, note, rows }: { id: string; title: string; note: string; rows: [string, ReactNode | null | undefined][] }) {
  return <section aria-labelledby={id} className="rounded-lg border px-3 py-2.5">
    <h3 id={id} className={legendClass}>{title}</h3>
    <p className="mt-0.5 text-xs text-faint">{note}</p>
    <dl className="mt-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1 text-xs">
      {rows.filter(([, value]) => value).map(([label, value]) => <div key={label} className="contents">
        <dt className="font-mono text-[0.6875rem] tracking-wider text-faint uppercase">{label}</dt>
        <dd className="min-w-0 font-mono break-words">{value}</dd>
      </div>)}
    </dl>
  </section>;
}

function Conversion({ detail }: { detail: TransactionDetail }) {
  const t = useTranslations("editor.conversion");
  const conversion = detail.conversion;
  if (!conversion) return null;
  return <SourceDetails id="conversion-heading" title={t("title")} note={t("note")} rows={[
    [t("original"), formatMoney(conversion.originalAmount, conversion.originalCurrency)],
    ["EUR", formatEURCents(conversion.eurCents)],
    [t("rate"), t("rateValue", { rate: conversion.rate, currency: conversion.originalCurrency, date: conversion.rateDate })],
    [t("source"), conversion.providerPolicy],
    [t("rounding"), conversion.roundingRule === "half_away_from_zero" ? t("halfAwayFromZero") : t("otherRule", { rule: conversion.roundingRule.replaceAll("_", " ") })],
  ]} />;
}

function ImportedOriginal({ detail }: { detail: TransactionDetail }) {
  const t = useTranslations("editor.original");
  const original = detail.importedOriginal;
  if (!original) return null;
  return <SourceDetails id="original-heading" title={t("title")} note={t("note")} rows={[
    [t("amount"), `${original.amount} ${original.currency}`],
    [t("time"), original.timestamp],
    [t("action"), original.action],
    [t("merchant"), original.merchantName],
    [t("category"), original.merchantCategory],
    [t("fee"), original.feeAmount && `${original.feeAmount} ${original.feeCurrency}`],
    [t("notes"), original.notes],
  ]} />;
}

function EditorForm({ detail, accounts, categories, account, timezone, onDone }: {
  detail?: TransactionDetail; accounts: Account[]; categories: Entity[]; account?: number; timezone: string;
  onDone: (message: string, suggestion: RuleSuggestion | null) => void;
}) {
  const t = useTranslations("editor");
  const common = useTranslations("common");
  const client = useQueryClient();
  const [status, setStatus] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  // Manual entries are in EUR; a saved transaction can only move to an account in its own currency.
  const currency = detail?.currency ?? "EUR";
  const converted = Boolean(detail?.conversion);
  const eligible = accounts.filter((item) => item.currency === currency && (!item.archived || item.id === detail?.accountId));
  const uncategorized = categories.find((category) => category.system);
  const form = useForm<Values>({ defaultValues: initialValues(detail, eligible, account, timezone, uncategorized?.id) });
  const splits = useFieldArray({ control: form.control, name: "splits" });
  const { errors } = form.formState;
  const kind = useWatch({ control: form.control, name: "kind" });
  const accountId = useWatch({ control: form.control, name: "accountId" });
  const payee = useWatch({ control: form.control, name: "payee" });
  const linkedTransfer = Boolean(detail?.transferCounterpart);
  const allowed: Kind[] = linkedTransfer ? ["transfer"] : detail && !detail.imported && detail.kind !== "transfer"
    ? ["expense", "income", "refund"] : ["expense", "income", "transfer", "refund"];
  // A converted amount keeps its sign, so only types with the same direction apply.
  const outgoing = detail?.amount.startsWith("-");
  const choices = converted ? allowed.filter((value) => value === "transfer" || (outgoing ? value === "expense" : value !== "expense")) : allowed;
  const splitting = (kind === "expense" || kind === "income") && splits.fields.length > 0;

  const refresh = () => {
    for (const key of ledgerQueries) void client.invalidateQueries({ queryKey: [key] });
  };
  const serverFields = (): Path<Values>[] => [
    "kind", "accountId", "toAccountId", "date", "amount", "payee", "categoryId", "refundOf", "notes", "splits",
    ...splits.fields.flatMap((_, index) => [`splits.${index}.categoryId`, `splits.${index}.amount`, `splits.${index}.notes`] as Path<Values>[]),
  ];
  // An imported transaction can confirm its whole import in the same save.
  const reviewing = detail?.importBatch?.reviewStatus === "needs_review";
  const save = useMutation({
    mutationFn: async ({ values, confirmImport }: { values: Values; confirmImport: boolean }) => {
      const input = toInput(values, detail?.revision);
      if (!detail) return { transaction: await createTransaction(input), ruleSuggestion: null };
      return updateTransaction(detail.id, confirmImport ? { ...input, confirmImport } : input);
    },
    onSuccess: (result, { confirmImport }) => {
      refresh();
      if (confirmImport) {
        void client.invalidateQueries({ queryKey: ["import"] });
        void client.invalidateQueries({ queryKey: ["imports"] });
        void client.invalidateQueries({ queryKey: ["dashboard"] });
        void client.invalidateQueries({ queryKey: ["notifications"] });
        void client.invalidateQueries({ queryKey: ["rules"] });
      }
      const amount = formatMoney(magnitude(result.transaction.amount), result.transaction.currency);
      const saved = detail ? t("saved", { amount, date: result.transaction.date }) : t("recorded", { amount, account: result.transaction.accountName });
      onDone(confirmImport ? `${saved} ${t("confirmedImport")}` : saved, result.ruleSuggestion);
    },
    onError: (error) => setStatus(showServerErrors(error, form.setError, serverFields())),
  });
  const remove = useMutation({
    mutationFn: () => deleteTransaction(detail!.id, detail!.revision),
    onSuccess: () => { refresh(); onDone(t("deleted"), null); },
    onError: (error) => { setConfirmDelete(false); setStatus(error.message); },
  });
  const describe = (id: string, error: unknown, description = false) =>
    [description && `${id}-description`, error ? `${id}-error` : ""].filter(Boolean).join(" ") || undefined;

  if (eligible.length === 0) {
    return <p className="text-[0.8125rem] text-muted-foreground">
      {t.rich("needsEurAccount", { link: (chunks) => <Link href="/accounts/" className="underline underline-offset-4">{chunks}</Link> })}
    </p>;
  }
  const submit = (confirmImport: boolean) => form.handleSubmit((values) => { setStatus(""); save.mutate({ values, confirmImport }); });
  return <form noValidate className={cn("flex flex-col gap-4", compactFields)} onSubmit={submit(false)}>
    <FieldGroup className="grid gap-3 sm:grid-cols-2">
      <Controller control={form.control} name="kind" render={({ field }) =>
        <FieldSet className="sm:col-span-2">
          <FieldLegend variant="label" className={labelLegend}>{t("type")}</FieldLegend>
          <RadioGroup value={field.value} onValueChange={field.onChange} className="flex flex-wrap gap-x-5 gap-y-2" aria-invalid={Boolean(errors.kind)}>
            {choices.map((value) => <Field key={value} orientation="horizontal" className="w-auto">
              <RadioGroupItem id={`editor-kind-${value}`} value={value} />
              <FieldLabel htmlFor={`editor-kind-${value}`} className="text-[0.8125rem] font-normal text-foreground">{common(`kinds.${value}`)}</FieldLabel>
            </Field>)}
          </RadioGroup>
          {errors.kind && <FieldError>{errors.kind.message}</FieldError>}
        </FieldSet>} />
      <Field data-invalid={Boolean(errors.accountId)}>
        <FieldLabel htmlFor="editor-account">{kind === "transfer" && !linkedTransfer ? t("fromAccount") : t("account")}</FieldLabel>
        <NativeSelect id="editor-account" size="sm" className="w-full" aria-invalid={Boolean(errors.accountId)} aria-describedby={describe("editor-account", errors.accountId)}
          {...form.register("accountId", { required: t("chooseAccountError") })}>
          {eligible.map((item) => <NativeSelectOption key={item.id} value={item.id}>{item.name}</NativeSelectOption>)}
        </NativeSelect>
        {errors.accountId && <FieldError id="editor-account-error">{errors.accountId.message}</FieldError>}
      </Field>
      {kind === "transfer" && !detail && <Field data-invalid={Boolean(errors.toAccountId)}>
        <FieldLabel htmlFor="editor-to-account">{t("toAccount")}</FieldLabel>
        <NativeSelect id="editor-to-account" size="sm" className="w-full" aria-invalid={Boolean(errors.toAccountId)}
          aria-describedby={describe("editor-to-account", errors.toAccountId)}
          {...form.register("toAccountId", { validate: (value, all) => all.kind !== "transfer" || Boolean(value) || t("chooseReceiving") })}>
          <NativeSelectOption value="">{t("chooseAccount")}</NativeSelectOption>
          {eligible.filter((item) => String(item.id) !== accountId).map((item) =>
            <NativeSelectOption key={item.id} value={item.id}>{item.name}</NativeSelectOption>)}
        </NativeSelect>
        {errors.toAccountId && <FieldError id="editor-to-account-error">{errors.toAccountId.message}</FieldError>}
      </Field>}
      {linkedTransfer && detail?.transferCounterpart && <p className="self-end text-xs text-muted-foreground">
        {t("linked", { account: detail.transferCounterpart.accountName })}
      </p>}
      <Field data-invalid={Boolean(errors.date)}>
        <FieldLabel htmlFor="editor-date">{t("date")}</FieldLabel>
        <Input id="editor-date" type="date" className={cn(inputClass, "font-mono")} aria-invalid={Boolean(errors.date)} aria-describedby={describe("editor-date", errors.date)}
          {...form.register("date", { required: t("enterDate") })} />
        {errors.date && <FieldError id="editor-date-error">{errors.date.message}</FieldError>}
      </Field>
      <Field data-invalid={Boolean(errors.amount)}>
        <FieldLabel htmlFor="editor-amount">{t("amountIn", { currency })}</FieldLabel>
        <Input id="editor-amount" inputMode="decimal" className={cn(inputClass, "font-mono")} autoComplete="off" placeholder={t("amountPlaceholder")} aria-invalid={Boolean(errors.amount)}
          readOnly={converted} aria-describedby={describe("editor-amount", errors.amount, converted)}
          {...form.register("amount", { required: t("enterAmount"), pattern: { value: amountPattern, message: t("amountFormat") } })} />
        {converted && <FieldDescription id="editor-amount-description">{t("convertedHint")}</FieldDescription>}
        {errors.amount && <FieldError id="editor-amount-error">{errors.amount.message}</FieldError>}
      </Field>
      {kind !== "transfer" && <Controller control={form.control} name="payee" render={({ field }) =>
        <Field data-invalid={Boolean(errors.payee)}>
          <FieldLabel htmlFor="editor-payee">{t("payee")}</FieldLabel>
          <PayeeInput id="editor-payee" value={field.value} onChange={field.onChange} invalid={Boolean(errors.payee)}
            describedBy={describe("editor-payee", errors.payee, true)} />
          <FieldDescription id="editor-payee-description">{t("payeeHint")}</FieldDescription>
          {errors.payee && <FieldError id="editor-payee-error">{errors.payee.message}</FieldError>}
        </Field>} />}
      {kind === "refund" && <Controller control={form.control} name="refundOf" render={({ field }) =>
        <RefundTarget value={field.value} onChange={field.onChange} payee={payee} current={detail?.refundOfTransaction ?? null}
          invalid={Boolean(errors.refundOf)} />} />}
      {errors.refundOf && <FieldError className="sm:col-span-2">{errors.refundOf.message}</FieldError>}
      {kind !== "transfer" && !splitting && <Field data-invalid={Boolean(errors.categoryId)}>
        <FieldLabel htmlFor="editor-category">{t("category")}</FieldLabel>
        <NativeSelect id="editor-category" size="sm" className="w-full" aria-invalid={Boolean(errors.categoryId)}
          aria-describedby={describe("editor-category", errors.categoryId, true)} {...form.register("categoryId")}>
          <NativeSelectOption value="">{kind === "refund" ? t("sameAsPurchase") : detail ? uncategorized?.name ?? t("uncategorized") : t("payeeRule")}</NativeSelectOption>
          {categories.filter((category) => !detail || kind === "refund" || category !== uncategorized).map((category) => <NativeSelectOption key={category.id} value={category.id}>{category.name}</NativeSelectOption>)}
        </NativeSelect>
        <FieldDescription id="editor-category-description">
          {kind === "refund" ? t("refundCategoryHint") : t("categoryHint")}
        </FieldDescription>
        {errors.categoryId && <FieldError id="editor-category-error">{errors.categoryId.message}</FieldError>}
      </Field>}
      {(kind === "expense" || kind === "income") && <FieldSet className="sm:col-span-2">
        <FieldLegend variant="label" className={labelLegend}>{t("split")}</FieldLegend>
        {splits.fields.length === 0 ? <div className="flex flex-wrap items-center gap-3">
          <Button type="button" variant="outline" size="sm" onClick={() => {
            const category = form.getValues("categoryId");
            splits.replace([{ categoryId: category, amount: form.getValues("amount").trim(), notes: "" }, { categoryId: "", amount: "", notes: "" }]);
          }}><Plus data-icon="inline-start" aria-hidden="true" />{t("splitAcross")}</Button>
          <span className="text-xs text-muted-foreground">{t("splitHint")}</span>
        </div> : <div className="flex flex-col gap-2">
          {splits.fields.map((split, index) => {
            const fieldErrors = errors.splits?.[index];
            return <div key={split.id} className="grid gap-2 sm:grid-cols-[1fr_8rem_1fr_auto] sm:items-start">
              <Field data-invalid={Boolean(fieldErrors?.categoryId)}>
                <FieldLabel htmlFor={`split-${index}-category`} className="sr-only">{t("partCategory", { number: index + 1 })}</FieldLabel>
                <NativeSelect id={`split-${index}-category`} size="sm" className="w-full" aria-invalid={Boolean(fieldErrors?.categoryId)}
                  {...form.register(`splits.${index}.categoryId`, { required: t("chooseCategoryError") })}>
                  <NativeSelectOption value="">{t("chooseCategory")}</NativeSelectOption>
                  {categories.map((category) => <NativeSelectOption key={category.id} value={category.id}>{category.name}</NativeSelectOption>)}
                </NativeSelect>
                {fieldErrors?.categoryId && <FieldError>{fieldErrors.categoryId.message}</FieldError>}
              </Field>
              <Field data-invalid={Boolean(fieldErrors?.amount)}>
                <FieldLabel htmlFor={`split-${index}-amount`} className="sr-only">{t("partAmount", { number: index + 1 })}</FieldLabel>
                <Input id={`split-${index}-amount`} inputMode="decimal" className={cn(inputClass, "font-mono")} autoComplete="off" placeholder={t("amountPlaceholder")} aria-invalid={Boolean(fieldErrors?.amount)}
                  {...form.register(`splits.${index}.amount`, { required: t("enterAmount"), pattern: { value: amountPattern, message: t("amountFormat") } })} />
                {fieldErrors?.amount && <FieldError>{fieldErrors.amount.message}</FieldError>}
              </Field>
              <Field>
                <FieldLabel htmlFor={`split-${index}-notes`} className="sr-only">{t("partNotes", { number: index + 1 })}</FieldLabel>
                <Input id={`split-${index}-notes`} className={inputClass} placeholder={t("notes")} maxLength={1000} autoComplete="off" {...form.register(`splits.${index}.notes`)} />
              </Field>
              <Button type="button" variant="ghost" size="icon-sm" className="size-8" aria-label={t("removePart", { number: index + 1 })}
                onClick={() => splits.fields.length <= 2 ? splits.remove() : splits.remove(index)}><Trash2 aria-hidden="true" /></Button>
            </div>;
          })}
          <div className="flex flex-wrap gap-2">
            <Button type="button" variant="outline" size="sm" disabled={splits.fields.length >= 50}
              onClick={() => splits.append({ categoryId: "", amount: "", notes: "" })}><Plus data-icon="inline-start" aria-hidden="true" />{t("addPart")}</Button>
            <Button type="button" variant="ghost" size="sm" onClick={() => splits.remove()}>{t("removeSplit")}</Button>
          </div>
          {errors.splits?.message && <FieldError>{errors.splits.message}</FieldError>}
          {errors.splits?.root?.message && <FieldError>{errors.splits.root.message}</FieldError>}
        </div>}
      </FieldSet>}
      <Field data-invalid={Boolean(errors.notes)} className="sm:col-span-2">
        <FieldLabel htmlFor="editor-notes">{t("notes")}</FieldLabel>
        <Input id="editor-notes" className={inputClass} maxLength={1000} autoComplete="off" aria-invalid={Boolean(errors.notes)}
          aria-describedby={describe("editor-notes", errors.notes)} {...form.register("notes")} />
        {errors.notes && <FieldError id="editor-notes-error">{errors.notes.message}</FieldError>}
      </Field>
    </FieldGroup>
    {detail && detail.refunds.length > 0 && <p className="text-xs text-muted-foreground">
      {t("refunded", { amount: formatMoney(detail.refundedTotal, detail.currency), count: detail.refunds.length })}
    </p>}
    {detail && <Conversion detail={detail} />}
    {detail && <ImportedOriginal detail={detail} />}
    {detail && detail.editCount > 0 && <p className="text-xs text-muted-foreground">{t("edited", { count: detail.editCount })}</p>}
    {reviewing && <p className="text-xs text-muted-foreground">
      {t("confirmHint")}
    </p>}
    <div aria-live="polite">
      {status && <Alert role="alert" variant="destructive" className={alertClass}>
        <AlertDescription className={alertDescriptionClass}>{status}</AlertDescription>
      </Alert>}
    </div>
    <DialogFooter className="px-4 py-3 sm:items-center sm:justify-between">
      {detail ? confirmDelete ? <div className="flex flex-wrap items-center gap-2">
        <span className="text-xs">{linkedTransfer ? t("deleteTransfer") : t("deleteTransaction")}</span>
        <Button type="button" variant="destructive" size="sm" disabled={remove.isPending} onClick={() => remove.mutate()}>
          {remove.isPending ? t("deleting") : t("delete")}
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={() => setConfirmDelete(false)}>{t("keep")}</Button>
      </div> : <Button type="button" variant="ghost" size="sm" className="self-start text-destructive" onClick={() => setConfirmDelete(true)}>{t("delete")}</Button> : <span />}
      <div className="flex flex-wrap justify-end gap-2">
        {reviewing && <Button type="button" variant="outline" size="sm" disabled={save.isPending} onClick={submit(true)}>
          {save.isPending && save.variables?.confirmImport ? t("savingConfirming") : t("saveConfirm")}
        </Button>}
        <Button type="submit" size="sm" disabled={save.isPending}>
          {save.isPending && !save.variables?.confirmImport ? common("saving") : detail ? t("saveChanges") : t("saveEntry")}
        </Button>
      </div>
    </DialogFooter>
  </form>;
}

export type EditorTarget = { mode: "new" } | { mode: "edit"; id: number };

export function TransactionEditor({ target, onClose, accounts, categories, account, timezone, onDone }: {
  target: EditorTarget | null; onClose: () => void; accounts: Account[]; categories: Entity[]; account?: number; timezone: string;
  onDone: (message: string, suggestion: RuleSuggestion | null) => void;
}) {
  const t = useTranslations("editor");
  // The last target stays on screen while the dialog animates closed.
  const [shown, setShown] = useState(target);
  if (target !== null && target !== shown) setShown(target);
  // Radix returns focus only to a dialog trigger. The editor opens from
  // shortcuts and row buttons too, so it returns focus to whatever opened it.
  const opener = useRef<HTMLElement | null>(null);
  const id = shown?.mode === "edit" ? shown.id : undefined;
  const detail = useQuery({
    queryKey: ["transaction", id],
    queryFn: ({ signal }) => getTransaction(id!, { signal }),
    enabled: target !== null && id !== undefined,
    staleTime: 0,
  });
  return <Dialog open={target !== null} onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl"
      onOpenAutoFocus={() => { opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null; }}
      onCloseAutoFocus={(event) => {
        if (!opener.current?.isConnected) return;
        event.preventDefault();
        opener.current.focus({ focusVisible: true });
      }}>
      <DialogHeader className="gap-1.5 border-b pb-3 -mx-4 px-4">
        <p className={legendClass}>{id === undefined ? t("newEntry") : t("transaction", { id })}</p>
        <DialogTitle>{id === undefined ? t("recordTitle") : t("editTitle")}</DialogTitle>
        <DialogDescription className="text-xs">
          {id === undefined ? t("recordDescription") : detail.data?.imported ? t("importedDescription") : t("editDescription")}
        </DialogDescription>
        {detail.data && detail.data.reviewStatus !== "confirmed" && <Badge variant={detail.data.reviewStatus === "source_conflict" ? "destructive" : "warning"}>
          {detail.data.reviewStatus === "source_conflict" ? t("sourceConflict") : t("needsReview")}
        </Badge>}
      </DialogHeader>
      {id === undefined ? <EditorForm accounts={accounts} categories={categories} account={account} timezone={timezone} onDone={onDone} /> :
        detail.isPending ? <div role="status" aria-label={t("loading")} className="grid gap-3 sm:grid-cols-2">
          {Array.from({ length: 6 }, (_, index) => <Skeleton key={index} className="h-12" />)}
        </div> :
        detail.isError ? <p role="alert" className="text-[0.8125rem] text-destructive">{detail.error.message}</p> :
        <EditorForm key={`${detail.data.id}-${detail.data.revision}`} detail={detail.data} accounts={accounts} categories={categories}
          timezone={timezone} onDone={onDone} />}
    </DialogContent>
  </Dialog>;
}

// RuleOffer appears after a category correction. Creating the rule affects
// future entries only; existing ones change when the owner selects them.
export function RuleOffer({ suggestion, onDismiss }: { suggestion: RuleSuggestion; onDismiss: () => void }) {
  const t = useTranslations("editor.rule");
  const names = { payee: suggestion.payeeName, category: suggestion.categoryName };
  const client = useQueryClient();
  const create = useMutation({
    mutationFn: () => saveRule({ payeeId: suggestion.payeeId, categoryId: suggestion.categoryId }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["rules"] });
      void client.invalidateQueries({ queryKey: ["payees"] });
    },
  });
  return <Alert className={alertClass}>
    <AlertTitle>{create.isSuccess ? t("created") : t("offer", names)}</AlertTitle>
    <AlertDescription className={cn(alertDescriptionClass, "flex flex-col gap-2.5")}>
      <p>
        {create.isSuccess ? t("createdBody", names) : t("offerBody", names)}
        {suggestion.otherTransactions > 0 && <> {t.rich("others", {
          count: suggestion.otherTransactions,
          link: (chunks) => <Link className="underline underline-offset-4" href={`/transactions/?payee=${suggestion.payeeId}`}>{chunks}</Link>,
        })}</>}
      </p>
      {create.isError && <p role="alert" className="text-xs text-destructive">{create.error.message}</p>}
      <div className="flex flex-wrap gap-2">
        {!create.isSuccess && <Button size="xs" disabled={create.isPending} onClick={() => create.mutate()}>
          {create.isPending ? t("creating") : t("create")}
        </Button>}
        <Button size="xs" variant="ghost" onClick={onDismiss}>{create.isSuccess ? t("close") : t("notNow")}</Button>
      </div>
    </AlertDescription>
  </Alert>;
}
