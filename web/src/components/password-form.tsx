"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { useDocumentTitle } from "@/components/locale";
import { changePassword, signOut, type Session } from "@/lib/api";
import { useSetSession } from "@/lib/session";
import { showServerErrors } from "@/lib/forms";
import { alertClass, alertDescriptionClass, compactFields, footerClass, legendClass, PanelHeader } from "@/components/panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

type Values = { currentPassword: string; newPassword: string; confirmation: string };

// PasswordFields replaces the signed-in user's password. The server signs out
// their other sessions and keeps this one.
function PasswordFields({ id, autoFocus, onChanged }: { id: string; autoFocus?: boolean; onChanged: (session: Session) => void }) {
  const t = useTranslations("auth");
  const [message, setMessage] = useState("");
  const form = useForm<Values>({ defaultValues: { currentPassword: "", newPassword: "", confirmation: "" } });
  const { errors } = form.formState;
  const mutation = useMutation({
    mutationFn: ({ currentPassword, newPassword }: Values) => changePassword({ currentPassword, newPassword }),
    onSuccess: (session) => {
      form.reset();
      onChanged(session);
    },
    onError: (error) => setMessage(showServerErrors(error, form.setError, ["currentPassword", "newPassword"])),
  });
  const describe = (field: string, hasDescription: boolean, error: unknown) =>
    [hasDescription && `${id}-${field}-description`, error ? `${id}-${field}-error` : ""].filter(Boolean).join(" ") || undefined;
  return <form noValidate aria-labelledby={`${id}-heading`} className={cn("flex flex-col gap-4", compactFields)}
    onSubmit={form.handleSubmit((values) => { setMessage(""); mutation.mutate(values); })}>
    {message && <Alert variant="destructive" className={alertClass}>
      <AlertDescription role="alert" className={alertDescriptionClass}>{message}</AlertDescription>
    </Alert>}
    <FieldGroup className="gap-3">
      <Field data-invalid={Boolean(errors.currentPassword)}>
        <FieldLabel htmlFor={`${id}-current`}>{t("currentPassword")}</FieldLabel>
        <Input id={`${id}-current`} type="password" autoComplete="current-password" autoFocus={autoFocus}
          aria-invalid={Boolean(errors.currentPassword)} aria-describedby={describe("current", false, errors.currentPassword)}
          {...form.register("currentPassword", { required: t("enterPassword") })} />
        {errors.currentPassword && <FieldError id={`${id}-current-error`}>{errors.currentPassword.message}</FieldError>}
      </Field>
      <Field data-invalid={Boolean(errors.newPassword)}>
        <FieldLabel htmlFor={`${id}-new`}>{t("newPassword")}</FieldLabel>
        <Input id={`${id}-new`} type="password" autoComplete="new-password" aria-invalid={Boolean(errors.newPassword)}
          aria-describedby={describe("new", true, errors.newPassword)}
          {...form.register("newPassword", {
            required: t("choosePassword"),
            minLength: { value: 12, message: t("passwordMin") },
            maxLength: { value: 256, message: t("passwordMax") },
          })} />
        <FieldDescription id={`${id}-new-description`}>{t("passwordHint")}</FieldDescription>
        {errors.newPassword && <FieldError id={`${id}-new-error`}>{errors.newPassword.message}</FieldError>}
      </Field>
      <Field data-invalid={Boolean(errors.confirmation)}>
        <FieldLabel htmlFor={`${id}-confirmation`}>{t("confirmPassword")}</FieldLabel>
        <Input id={`${id}-confirmation`} type="password" autoComplete="new-password" aria-invalid={Boolean(errors.confirmation)}
          aria-describedby={describe("confirmation", false, errors.confirmation)}
          {...form.register("confirmation", {
            required: t("enterPasswordAgain"),
            validate: (value, values) => value === values.newPassword || t("passwordsDiffer"),
          })} />
        {errors.confirmation && <FieldError id={`${id}-confirmation-error`}>{errors.confirmation.message}</FieldError>}
      </Field>
    </FieldGroup>
    <Button type="submit" size="sm" className="w-fit" disabled={mutation.isPending}>
      {mutation.isPending ? t("changingPassword") : t("changePassword")}
    </Button>
  </form>;
}

// ChangePasswordPage is where a user signed in with a temporary password
// must choose their own before anything else.
export function ChangePasswordPage() {
  const t = useTranslations("auth");
  const app = useTranslations("app");
  useDocumentTitle(app("pages.changePassword"));
  const shell = useTranslations("shell");
  const client = useQueryClient();
  const setSession = useSetSession();
  const leave = useMutation({ mutationFn: signOut, onSuccess: () => setSession({ setupRequired: false, authenticated: false, user: null }) });
  return <div className="mx-auto flex w-full max-w-sm flex-col gap-4">
    <div className="flex flex-col gap-1">
      <p className={legendClass}>{t("firstSignIn")}</p>
      <h1 id="forced-password-heading" className="text-xl font-semibold tracking-tight">{t("chooseOwnPassword")}</h1>
      <p className="text-sm text-muted-foreground">{t("chooseOwnPasswordHint")}</p>
    </div>
    <Card size="sm" className="gap-0 py-0">
      <CardContent className="px-3.5 py-4">
        {/* The shell leaves this page once the session no longer asks for a new password. */}
        <PasswordFields id="forced-password" autoFocus onChanged={(session) => client.setQueryData(["session"], session)} />
      </CardContent>
      <CardFooter className={footerClass}>{t("chooseOwnPasswordFooter")}</CardFooter>
    </Card>
    <Button variant="ghost" size="sm" className="w-fit text-muted-foreground" disabled={leave.isPending} onClick={() => leave.mutate()}>
      {shell("signOut")}
    </Button>
    {leave.isError && <p role="alert" className="text-xs text-destructive">{leave.error.message}</p>}
  </div>;
}

export function PasswordSettings() {
  const t = useTranslations("settings");
  const client = useQueryClient();
  const [status, setStatus] = useState("");
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="password-heading" title={t("password")} />
    <CardContent className="flex flex-col gap-2 px-3.5 py-3">
      <PasswordFields id="password" onChanged={() => {
        setStatus(t("passwordChanged"));
        void client.invalidateQueries({ queryKey: ["sessions"] });
      }} />
      <p aria-live="polite" className="text-xs text-muted-foreground empty:hidden">{status}</p>
    </CardContent>
  </Card>;
}
