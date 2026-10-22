"use client";

import { useMutation } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { useDocumentTitle } from "@/components/locale";
import { ArrowRight } from "lucide-react";
import { setUp } from "@/lib/api";
import { defaultTimezone } from "@/lib/format";
import { showServerErrors } from "@/lib/forms";
import { useSession, useSetSession } from "@/lib/session";
import { IntegrationSettings } from "@/components/integration-settings";
import { TimezoneSelect } from "@/components/timezone-select";
import { alertClass, alertDescriptionClass, compactFields, footerClass, legendClass, PanelHeader } from "@/components/panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

type OwnerValues = { email: string; password: string; confirmation: string; timezone: string };

function OwnerStep() {
  const t = useTranslations("auth");
  const setSession = useSetSession();
  const [message, setMessage] = useState("");
  const form = useForm<OwnerValues>({ defaultValues: { email: "", password: "", confirmation: "", timezone: defaultTimezone } });
  const { errors } = form.formState;
  const mutation = useMutation({
    mutationFn: ({ email, password, timezone }: OwnerValues) => setUp({ email, password, timezone }),
    onSuccess: setSession,
    onError: (error) => setMessage(showServerErrors(error, form.setError, ["email", "password", "timezone"])),
  });
  const describe = (id: string, hasDescription: boolean, error: unknown) =>
    [hasDescription && `${id}-description`, error ? `${id}-error` : ""].filter(Boolean).join(" ") || undefined;
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="owner-step-heading" title={t("createOwner")} />
    <CardContent className="px-3.5 py-4">
      <form noValidate aria-labelledby="owner-step-heading" className={cn("flex flex-col gap-4", compactFields)}
        onSubmit={form.handleSubmit((values) => { setMessage(""); mutation.mutate(values); })}>
        {message && <Alert variant="destructive" className={alertClass}>
          <AlertDescription role="alert" className={alertDescriptionClass}>{message}</AlertDescription>
        </Alert>}
        <FieldGroup className="grid gap-4 sm:grid-cols-2">
          <Field data-invalid={Boolean(errors.email)} className="sm:col-span-2">
            <FieldLabel htmlFor="email">{t("email")}</FieldLabel>
            <Input id="email" type="email" autoComplete="username" autoFocus aria-invalid={Boolean(errors.email)}
              aria-describedby={describe("email", false, errors.email)}
              {...form.register("email", { required: t("enterEmail") })} />
            {errors.email && <FieldError id="email-error">{errors.email.message}</FieldError>}
          </Field>
          <Field data-invalid={Boolean(errors.password)}>
            <FieldLabel htmlFor="password">{t("password")}</FieldLabel>
            <Input id="password" type="password" autoComplete="new-password" aria-invalid={Boolean(errors.password)}
              aria-describedby={describe("password", true, errors.password)}
              {...form.register("password", {
                required: t("choosePassword"),
                minLength: { value: 12, message: t("passwordMin") },
                maxLength: { value: 256, message: t("passwordMax") },
              })} />
            <FieldDescription id="password-description">{t("passwordHint")}</FieldDescription>
            {errors.password && <FieldError id="password-error">{errors.password.message}</FieldError>}
          </Field>
          <Field data-invalid={Boolean(errors.confirmation)}>
            <FieldLabel htmlFor="confirmation">{t("confirmPassword")}</FieldLabel>
            <Input id="confirmation" type="password" autoComplete="new-password" aria-invalid={Boolean(errors.confirmation)}
              aria-describedby={describe("confirmation", false, errors.confirmation)}
              {...form.register("confirmation", {
                required: t("enterPasswordAgain"),
                validate: (value, values) => value === values.password || t("passwordsDiffer"),
              })} />
            {errors.confirmation && <FieldError id="confirmation-error">{errors.confirmation.message}</FieldError>}
          </Field>
          <Field data-invalid={Boolean(errors.timezone)} className="sm:col-span-2">
            <FieldLabel htmlFor="timezone">{t("timezone")}</FieldLabel>
            <Controller control={form.control} name="timezone" rules={{ required: t("chooseTimezone") }} render={({ field }) =>
              <TimezoneSelect id="timezone" aria-invalid={Boolean(errors.timezone)} aria-describedby={describe("timezone", true, errors.timezone)} {...field} />} />
            <FieldDescription id="timezone-description">{t("timezoneHint")}</FieldDescription>
            {errors.timezone && <FieldError id="timezone-error">{errors.timezone.message}</FieldError>}
          </Field>
        </FieldGroup>
        <Button type="submit" className="w-fit" disabled={mutation.isPending}>{mutation.isPending ? t("creatingAccount") : t("createAccount")}</Button>
      </form>
    </CardContent>
    <CardFooter className={footerClass}>{t("setupFooter")}</CardFooter>
  </Card>;
}

function IntegrationStep({ ownerEmail }: { ownerEmail: string }) {
  const t = useTranslations("auth");
  const router = useRouter();
  return <>
    <IntegrationSettings email={ownerEmail} owner />
    <div className="flex flex-wrap gap-2">
      <Button onClick={() => router.push("/accounts/")}>{t("continueToAccounts")}<ArrowRight data-icon="inline-end" aria-hidden="true" /></Button>
      <Button variant="ghost" onClick={() => router.push("/")}>{t("skip")}</Button>
    </div>
  </>;
}

const steps = ["owner", "services"] as const;

export function Onboarding() {
  const t = useTranslations("auth");
  const app = useTranslations("app");
  useDocumentTitle(app("pages.onboarding"));
  const session = useSession();
  const owner = session.data?.authenticated ? session.data.user : null;
  const step = owner ? 2 : 1;
  return <>
    <div className="flex flex-col gap-3">
      <ol aria-label={t("stepOf", { step, total: steps.length })} className="grid grid-cols-2 gap-2">
        {steps.map((label, index) => <li key={label} aria-current={index + 1 === step ? "step" : undefined} className="flex flex-col gap-1.5">
          <span aria-hidden="true" className={cn("h-0.5 rounded-full", index < step ? "bg-mark" : "bg-border")} />
          <span className={cn(legendClass, index + 1 === step && "text-foreground")}>{index + 1}. {t(`steps.${label}`)}</span>
        </li>)}
      </ol>
      <div className="flex flex-col gap-1">
        <h1 className="text-xl font-semibold tracking-tight">{owner ? t("connectServices") : t("setUp")}</h1>
        <p className="text-sm text-muted-foreground">
          {owner ? t("servicesIntro") : t("ownerIntro")}
        </p>
      </div>
    </div>
    {owner ? <IntegrationStep ownerEmail={owner.email} /> : <OwnerStep />}
  </>;
}
