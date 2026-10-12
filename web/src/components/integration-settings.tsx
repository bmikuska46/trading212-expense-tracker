"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Controller, useForm, type FieldValues, type Path, type UseFormReturn } from "react-hook-form";
import { CircleAlert, CircleCheck, CircleHelp } from "lucide-react";
import { useTranslations } from "next-intl";
import {
  ApiError, checkIntegration, listIntegrations, removeIntegration, saveIntegration,
  type Check, type Integration, type Provider, type Trading212Sync,
} from "@/lib/api";
import { formatDateTime } from "@/lib/format";
import { monthName } from "@/components/scheduled-imports";
import { showServerErrors } from "@/lib/forms";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Skeleton } from "@/components/ui/skeleton";
import { alertClass, alertDescriptionClass, compactFields, footerClass, PanelHeader } from "@/components/panel";
import { cn } from "@/lib/utils";

function useReplaceIntegration() {
  const client = useQueryClient();
  return (updated: Integration) => client.setQueryData<Integration[]>(["integrations"],
    (current) => current?.map((item) => item.provider === updated.provider ? updated : item));
}

function StatusBadge({ integration }: { integration: Integration }) {
  const t = useTranslations("integrations.badges");
  if (!integration.configured) return <Badge variant="outline">{t("notConnected")}</Badge>;
  switch (integration.check?.status) {
    case "ok": return integration.enabled ? <Badge>{t("on")}</Badge> : <Badge variant="secondary">{t("off")}</Badge>;
    case "partial": return <Badge variant="warning">{t("partial")}</Badge>;
    case "failed": return <Badge variant="destructive">{t("failed")}</Badge>;
    default: return <Badge variant="outline">{t("unchecked")}</Badge>;
  }
}

function CheckResult({ check, checkedAt }: { check: Check; checkedAt?: string }) {
  const icons = { ok: CircleCheck, missing: CircleAlert, unverified: CircleHelp };
  const t = useTranslations("integrations");
  return <Alert variant={check.status === "failed" ? "destructive" : "default"} className={alertClass}>
    <AlertTitle>{check.message}</AlertTitle>
    <AlertDescription className={alertDescriptionClass}>
      {check.items.length > 0 && <ul className="mt-1 flex flex-col gap-1">
        {check.items.map((item) => {
          const Icon = icons[item.state];
          return <li key={item.key} className="flex items-start gap-2">
            <Icon className={cn("mt-0.5 size-3.5 shrink-0", item.state === "ok" ? "text-mark" : item.state === "missing" ? "text-spending" : "text-faint")} aria-hidden="true" />
            <span><span className="font-medium">{item.label}:</span> {t(`itemStates.${item.state}`)}{item.detail ? `. ${item.detail}` : ""}</span>
          </li>;
        })}
      </ul>}
      {checkedAt && <p className="mt-1.5 font-mono text-xs text-faint">{t("checked", { date: formatDateTime(checkedAt) })}</p>}
    </AlertDescription>
  </Alert>;
}

const inputClass = "h-8 text-[0.8125rem]";

type SecretField<T extends FieldValues> = { name: Path<T>; label: string; required: string; configured: boolean; hint?: string; description?: string };

function SecretInput<T extends FieldValues>({ form, field, id }: { form: UseFormReturn<T>; field: SecretField<T>; id: string }) {
  const t = useTranslations("integrations");
  const error = form.formState.errors[field.name];
  return <Field data-invalid={Boolean(error)}>
    <FieldLabel htmlFor={id}>{field.label}</FieldLabel>
    <Input id={id} type="password" autoComplete="off" spellCheck={false} aria-invalid={Boolean(error)} className={inputClass}
      aria-describedby={`${id}-description${error ? ` ${id}-error` : ""}`}
      placeholder={field.configured ? field.hint ? t("savedHint", { hint: field.hint }) : t("saved") : undefined}
      {...form.register(field.name, { required: field.configured ? false : field.required })} />
    <FieldDescription id={`${id}-description`}>{field.description ?? t("storedEncrypted")}</FieldDescription>
    {error && <FieldError id={`${id}-error`}>{String(error.message)}</FieldError>}
  </Field>;
}

function TextInput<T extends FieldValues>({ form, name, id, label, description, type = "text", required, autoComplete }: {
  form: UseFormReturn<T>; name: Path<T>; id: string; label: string; description?: string; type?: string; required?: string; autoComplete?: string;
}) {
  const error = form.formState.errors[name];
  const describedBy = [description && `${id}-description`, error && `${id}-error`].filter(Boolean).join(" ") || undefined;
  return <Field data-invalid={Boolean(error)}>
    <FieldLabel htmlFor={id}>{label}</FieldLabel>
    <Input id={id} type={type} autoComplete={autoComplete} aria-invalid={Boolean(error)} aria-describedby={describedBy} className={inputClass}
      {...form.register(name, { required })} />
    {description && <FieldDescription id={`${id}-description`}>{description}</FieldDescription>}
    {error && <FieldError id={`${id}-error`}>{String(error.message)}</FieldError>}
  </Field>;
}

function EnabledField<T extends FieldValues>({ form, id, label, description, name = "enabled", disabled, className = "sm:col-span-2" }: {
  form: UseFormReturn<T>; id: string; label: string; description: string; name?: string; disabled?: boolean; className?: string;
}) {
  return <Controller control={form.control} name={name as Path<T>} render={({ field }) =>
    <Field orientation="horizontal" data-disabled={disabled} className={className}>
      <Checkbox id={id} checked={Boolean(field.value) && !disabled} disabled={disabled} onCheckedChange={(checked) => field.onChange(checked === true)}
        aria-describedby={`${id}-description`} />
      <FieldContent>
        <FieldLabel htmlFor={id}>{label}</FieldLabel>
        <FieldDescription id={`${id}-description`}>{description}</FieldDescription>
      </FieldContent>
    </Field>} />;
}

function IntegrationCard<T extends FieldValues>({ integration, title, description, form, fields, save, children }: {
  integration: Integration; title: string; description: ReactNode; form: UseFormReturn<T>; fields: readonly Path<T>[];
  save: (values: T) => Promise<Integration>; children: ReactNode;
}) {
  const t = useTranslations("integrations");
  const replace = useReplaceIntegration();
  const [message, setMessage] = useState("");
  const [rejected, setRejected] = useState<Check | null>(null);
  const saving = useMutation({
    mutationFn: save,
    onSuccess: (updated) => { replace(updated); setRejected(null); setMessage(""); },
    onError: (error) => {
      setRejected(error instanceof ApiError && error.check ? error.check : null);
      setMessage(error instanceof ApiError && error.check ? "" : showServerErrors(error, form.setError, fields));
    },
  });
  const checking = useMutation({ mutationFn: () => checkIntegration(integration.provider), onSuccess: replace });
  const removing = useMutation({ mutationFn: () => removeIntegration(integration.provider), onSuccess: (updated) => { replace(updated); setRejected(null); } });
  const busy = saving.isPending || checking.isPending || removing.isPending;
  const failure = checking.error ?? removing.error;
  const headingId = `${integration.provider}-heading`;
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id={headingId} title={title} action={<StatusBadge integration={integration} />} />
    <form noValidate aria-labelledby={headingId} onSubmit={form.handleSubmit((values) => { setMessage(""); saving.mutate(values); })}>
      <CardContent className={cn("flex flex-col gap-3 px-3.5 py-3", compactFields)}>
        <p className="text-xs text-muted-foreground">{description}</p>
        {message && <Alert variant="destructive" className={alertClass}><AlertDescription role="alert" className={alertDescriptionClass}>{message}</AlertDescription></Alert>}
        <FieldGroup className="grid gap-3 sm:grid-cols-2">{children}</FieldGroup>
        <div aria-live="polite" className="flex flex-col gap-2 empty:hidden">
          {rejected ? <CheckResult check={rejected} /> : integration.check && <CheckResult check={integration.check} checkedAt={integration.checkedAt} />}
          {failure && <p role="alert" className="text-xs text-destructive">{failure.message}</p>}
        </div>
      </CardContent>
      <CardFooter className={cn(footerClass, "flex-wrap gap-2 py-2")}>
        <Button type="submit" size="sm" disabled={busy}>{saving.isPending ? t("checkingAndSaving") : t("checkAndSave")}</Button>
        {integration.configured && <>
          <Button type="button" size="sm" variant="outline" disabled={busy} onClick={() => checking.mutate()}>
            {checking.isPending ? t("checking") : t("checkAgain")}
          </Button>
          <Button type="button" size="sm" variant="ghost" className="text-muted-foreground" disabled={busy} onClick={() => removing.mutate()}>
            {removing.isPending ? t("removing") : t("remove")}
          </Button>
        </>}
      </CardFooter>
    </form>
  </Card>;
}

type Trading212Values = {
  environment: "live" | "demo"; apiKey: string; apiSecret: string; accountName: string; enabled: boolean; sync: Trading212Sync;
};

const allSync: Trading212Sync = { portfolio: true, pies: true, dividends: true, interest: true, cashback: true };

// The monthly card transaction import is the integration's enabled flag;
// the other parts run in the background whenever a key is saved.
function Trading212SyncFields({ form }: { form: UseFormReturn<Trading212Values> }) {
  const t = useTranslations("integrations.trading212");
  const portfolio = form.watch("sync.portfolio");
  const parts = ["portfolio", "pies", "dividends", "interest", "cashback"] as const;
  return <FieldSet className="gap-2 sm:col-span-2">
    <FieldLegend variant="label" className="mb-0 text-muted-foreground data-[variant=label]:text-xs">{t("sync")}</FieldLegend>
    <div className="grid gap-3 sm:grid-cols-2">
      <EnabledField form={form} id="t212-enabled" label={t("enabled")} description={t("enabledHint")} className="" />
      {parts.map((part) => <EnabledField key={part} form={form} id={`t212-sync-${part}`} name={`sync.${part}`} className=""
        label={t(`syncParts.${part}`)} description={t(`syncParts.${part}Hint`)} disabled={part === "pies" && !portfolio} />)}
    </div>
  </FieldSet>;
}

function Trading212Card({ integration }: { integration: Integration }) {
  const t = useTranslations("integrations");
  // A new connection syncs everything until the user turns parts off.
  const form = useForm<Trading212Values>({ defaultValues: {
    environment: integration.environment ?? "live", apiKey: "", apiSecret: "",
    accountName: integration.account?.name ?? "Trading 212", enabled: integration.configured ? integration.enabled : true,
    sync: integration.sync ?? allSync,
  } });
  const period = integration.earliestAutomaticPeriod;
  return <IntegrationCard integration={integration} title="Trading 212" form={form}
    fields={["environment", "apiKey", "apiSecret", "accountName"]}
    save={async (values) => { const saved = await saveIntegration("trading212", values); form.resetField("apiKey"); form.resetField("apiSecret"); return saved; }}
    description={<>{t("trading212.description")}{period && ` ${t("trading212.period", { period: monthName(period) })}`}</>}>
    <Controller control={form.control} name="environment" render={({ field }) =>
      <FieldSet className="gap-2 sm:col-span-2">
        <FieldLegend variant="label" className="mb-0 text-muted-foreground data-[variant=label]:text-xs">{t("trading212.environment")}</FieldLegend>
        <RadioGroup value={field.value} onValueChange={field.onChange} className="flex gap-5">
          {(["live", "demo"] as const).map((value) => <Field key={value} orientation="horizontal" className="w-auto">
            <RadioGroupItem id={`t212-environment-${value}`} value={value} />
            <FieldLabel htmlFor={`t212-environment-${value}`}>{t(`trading212.${value}`)}</FieldLabel>
          </Field>)}
        </RadioGroup>
      </FieldSet>} />
    <SecretInput form={form} id="t212-key" field={{ name: "apiKey", label: t("apiKey"), required: t("enterApiKey"), configured: integration.configured, hint: integration.apiKeyHint }} />
    <SecretInput form={form} id="t212-secret" field={{ name: "apiSecret", label: t("apiSecret"), required: t("enterApiSecret"), configured: integration.apiSecretConfigured }} />
    <TextInput form={form} name="accountName" id="t212-account" label={t("trading212.accountName")} autoComplete="off"
      description={t("trading212.accountNameHint")} />
    <Trading212SyncFields form={form} />
  </IntegrationCard>;
}

type TypeSafeValues = { apiKey: string; enabled: boolean; categorize: boolean };

function TypeSafeCard({ integration }: { integration: Integration }) {
  const t = useTranslations("integrations");
  const form = useForm<TypeSafeValues>({ defaultValues: { apiKey: "", enabled: integration.enabled, categorize: integration.categorize } });
  return <IntegrationCard integration={integration} title="TypeSafe" form={form} fields={["apiKey"]}
    save={async (values) => { const saved = await saveIntegration("typesafe", values); form.resetField("apiKey"); return saved; }}
    description={t("typesafe.description")}>
    <SecretInput form={form} id="typesafe-key" field={{ name: "apiKey", label: t("apiKey"), required: t("enterApiKey"), configured: integration.configured, hint: integration.apiKeyHint }} />
    <EnabledField form={form} id="typesafe-enabled" label={t("typesafe.enabled")}
      description={t("typesafe.enabledHint")} />
    <EnabledField form={form} id="typesafe-categorize" name="categorize" label={t("typesafe.categorize")}
      description={t("typesafe.categorizeHint")} />
  </IntegrationCard>;
}

type ResendValues = { apiKey: string; sender: string; recipient: string; applicationUrl: string; enabled: boolean };

function ResendCard({ integration, ownerEmail }: { integration: Integration; ownerEmail: string }) {
  const t = useTranslations("integrations");
  const form = useForm<ResendValues>({ defaultValues: {
    apiKey: "", sender: integration.sender, recipient: integration.recipient || ownerEmail,
    applicationUrl: integration.applicationUrl || window.location.origin, enabled: integration.enabled,
  } });
  return <IntegrationCard integration={integration} title="Resend" form={form} fields={["apiKey", "sender", "recipient", "applicationUrl"]}
    save={async (values) => { const saved = await saveIntegration("resend", values); form.resetField("apiKey"); return saved; }}
    description={t("resend.description")}>
    <SecretInput form={form} id="resend-key" field={{ name: "apiKey", label: t("apiKey"), required: t("enterApiKey"), configured: integration.configured, hint: integration.apiKeyHint,
      description: t("resend.keyHint") }} />
    <TextInput form={form} name="sender" id="resend-sender" label={t("resend.sender")} required={t("resend.enterSender")}
      description={t("resend.senderHint", { example: t("resend.senderExample") })} />
    <TextInput form={form} name="recipient" id="resend-recipient" label={t("resend.recipient")} type="email" autoComplete="email" required={t("resend.enterRecipient")} />
    <TextInput form={form} name="applicationUrl" id="resend-url" label={t("resend.url")} type="url" required={t("resend.enterUrl")}
      description={t("resend.urlHint")} />
    <EnabledField form={form} id="resend-enabled" label={t("resend.enabled")} description={t("resend.enabledHint")} />
  </IntegrationCard>;
}

// SharedCard shows a member the state of a service that the owner set up
// for everyone. Only the owner can change it.
function SharedCard({ integration, title, description, email }: { integration: Integration; title: string; description: string; email?: string }) {
  const t = useTranslations("integrations");
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader title={title} action={<StatusBadge integration={integration} />} />
    <CardContent className="flex flex-col gap-1.5 px-3.5 py-3 text-xs text-muted-foreground">
      <p>{description}</p>
      {email && integration.enabled && <p>{t("sharedRecipient", { email })}</p>}
    </CardContent>
    <CardFooter className={footerClass}>{t("shared")}</CardFooter>
  </Card>;
}

// IntegrationSettings lists the services. Trading 212 belongs to each user;
// TypeSafe and Resend are shared and only the owner changes them.
export function IntegrationSettings({ email, owner }: { email: string; owner: boolean }) {
  const t = useTranslations("integrations");
  const query = useQuery({ queryKey: ["integrations"], queryFn: listIntegrations });
  if (query.isPending) return <div role="status" aria-label={t("loading")} className="flex flex-col gap-3">
    {Array.from({ length: 3 }, (_, index) => <Skeleton key={index} className="h-48 rounded-xl" />)}
  </div>;
  if (query.isError) return <p role="alert" className="text-sm text-destructive">{query.error.message}</p>;
  const find = (provider: Provider) => query.data.find((item) => item.provider === provider);
  const trading212 = find("trading212"), typeSafe = find("typesafe"), resend = find("resend");
  return <div className="flex flex-col gap-3">
    {trading212 && <Trading212Card integration={trading212} />}
    {typeSafe && (owner ? <TypeSafeCard integration={typeSafe} />
      : <SharedCard integration={typeSafe} title="TypeSafe" description={t("typesafe.description")} />)}
    {resend && (owner ? <ResendCard integration={resend} ownerEmail={email} />
      : <SharedCard integration={resend} title="Resend" description={t("resend.description")} email={resend.recipient || email} />)}
  </div>;
}
