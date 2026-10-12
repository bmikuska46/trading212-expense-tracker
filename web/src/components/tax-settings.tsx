"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { getTaxSettings, updateTaxSettings, type IncomeKind, type TaxSettings } from "@/lib/api";
import { showServerErrors } from "@/lib/forms";
import { compactFields, PanelHeader } from "@/components/panel";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";

const kinds: IncomeKind[] = ["dividend", "interest", "cashback"];

function TaxForm({ saved }: { saved: TaxSettings }) {
  const t = useTranslations("settings.tax");
  const kind = useTranslations("investing.income.kinds");
  const client = useQueryClient();
  const [status, setStatus] = useState("");
  const form = useForm({ defaultValues: saved });
  const mutation = useMutation({
    mutationFn: updateTaxSettings,
    onSuccess: (updated) => {
      client.setQueryData(["tax-settings"], updated);
      void client.invalidateQueries({ queryKey: ["investment-income"] });
      form.reset(updated);
      setStatus(t("saved"));
    },
    onError: (failure) => setStatus(showServerErrors(failure, form.setError, kinds)),
  });
  return <form noValidate aria-labelledby="tax-heading" className={`flex flex-col gap-3 ${compactFields}`}
    onSubmit={form.handleSubmit((values) => { setStatus(""); mutation.mutate(values); })}>
    {kinds.map((name) => {
      const error = form.formState.errors[name];
      const id = `settings-tax-${name}`;
      return <Field key={name} data-invalid={Boolean(error)}>
        <FieldLabel htmlFor={id}>{t("label", { kind: kind(name) })}</FieldLabel>
        <Input id={id} type="number" inputMode="numeric" min={0} max={100} step={1} className="h-7 font-mono"
          aria-invalid={Boolean(error)} aria-describedby={error ? `${id}-error` : undefined}
          {...form.register(name, { valueAsNumber: true })} />
        {error && <FieldError id={`${id}-error`}>{error.message}</FieldError>}
      </Field>;
    })}
    <FieldDescription>{t("hint")}</FieldDescription>
    <div className="flex items-center gap-3">
      <Button type="submit" size="sm" variant="outline" disabled={mutation.isPending || !form.formState.isDirty}>
        {mutation.isPending ? t("saving") : t("save")}
      </Button>
      <p aria-live="polite" className="min-w-0 truncate text-xs text-muted-foreground">{status}</p>
    </div>
  </form>;
}

// TaxSettings sets the percentage the income page taxes dividends, interest,
// and cashback at, for the signed-in user's ledger.
export function TaxSettings() {
  const t = useTranslations("settings.tax");
  const query = useQuery({ queryKey: ["tax-settings"], queryFn: getTaxSettings });
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="tax-heading" title={t("title")} />
    <CardContent className="flex flex-col gap-3 px-3.5 py-3">
      {query.isPending ? <div role="status" aria-label={t("loading")} className="flex flex-col gap-2">
        {kinds.map((name) => <Skeleton key={name} className="h-7" />)}
      </div> :
        query.isError ? <p role="alert" className="text-xs text-destructive">{query.error.message}</p> :
          <TaxForm saved={query.data} />}
    </CardContent>
  </Card>;
}
