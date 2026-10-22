"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { updateTimezone, type Session, type User } from "@/lib/api";
import { showServerErrors } from "@/lib/forms";
import { isLocale, localeNames, locales } from "@/lib/locale";
import { useSession } from "@/lib/session";
import { IntegrationSettings } from "@/components/integration-settings";
import { PasswordSettings } from "@/components/password-form";
import { SelectCombobox } from "@/components/select-combobox";
import { SignIns } from "@/components/sign-ins";
import { UserSettings } from "@/components/user-settings";
import { setLocale, useLocaleSetting } from "@/components/locale";
import { compactFields, PanelHeader } from "@/components/panel";
import { TimezoneSelect } from "@/components/timezone-select";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Field, FieldDescription, FieldError, FieldLabel } from "@/components/ui/field";

// LocaleField switches the interface language at once. The choice belongs to
// this browser, so it needs no saving.
function LocaleField() {
  const t = useTranslations("settings");
  const locale = useLocaleSetting();
  return <Field>
    <FieldLabel htmlFor="settings-language">{t("language")}</FieldLabel>
    <SelectCombobox id="settings-language" size="sm" className="w-full" value={locale} aria-describedby="settings-language-description"
      onChange={(value) => { if (isLocale(value)) setLocale(value); }}
      options={locales.map((value) => ({ value, label: localeNames[value], lang: value }))} />
    <FieldDescription id="settings-language-description">{t("languageHint")}</FieldDescription>
  </Field>;
}

function TimezoneForm({ user }: { user: User }) {
  const t = useTranslations("settings");
  const client = useQueryClient();
  const [status, setStatus] = useState("");
  const form = useForm({ defaultValues: { timezone: user.timezone } });
  const error = form.formState.errors.timezone;
  const mutation = useMutation({
    mutationFn: ({ timezone }: { timezone: string }) => updateTimezone(timezone),
    onSuccess: (updated) => {
      client.setQueryData<Session>(["session"], (current) => current && { ...current, user: updated });
      form.reset({ timezone: updated.timezone });
      setStatus(t("timezoneSaved"));
    },
    onError: (failure) => setStatus(showServerErrors(failure, form.setError, ["timezone"])),
  });
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="account-heading" title={t("account")} action={<span className="max-w-44 truncate font-mono">{user.email}</span>} />
    <CardContent className={`flex flex-col gap-3 px-3.5 py-3 ${compactFields}`}>
      <LocaleField />
      <form noValidate aria-labelledby="account-heading" className={`flex flex-col gap-3 ${compactFields}`}
        onSubmit={form.handleSubmit((values) => { setStatus(""); mutation.mutate(values); })}>
        <Field data-invalid={Boolean(error)}>
          <FieldLabel htmlFor="settings-timezone">{t("timezone")}</FieldLabel>
          <Controller control={form.control} name="timezone" render={({ field }) =>
            <TimezoneSelect id="settings-timezone" size="sm" current={user.timezone} aria-invalid={Boolean(error)}
              aria-describedby={`settings-timezone-description${error ? " settings-timezone-error" : ""}`} {...field} />} />
          <FieldDescription id="settings-timezone-description">{t("timezoneHint")}</FieldDescription>
          {error && <FieldError id="settings-timezone-error">{error.message}</FieldError>}
        </Field>
        <div className="flex items-center gap-3">
          <Button type="submit" size="sm" variant="outline" disabled={mutation.isPending || !form.formState.isDirty}>
            {mutation.isPending ? t("saving") : t("saveTimezone")}
          </Button>
          <p aria-live="polite" className="min-w-0 truncate text-xs text-muted-foreground">{status}</p>
        </div>
      </form>
    </CardContent>
  </Card>;
}

export function AccountSettings() {
  const user = useSession().data?.user;
  if (!user) return null;
  return <>
    <TimezoneForm user={user} />
    <PasswordSettings />
    <SignIns />
  </>;
}

// Users is the owner's list of the people who use this workspace.
export function Users() {
  const user = useSession().data?.user;
  if (!user?.owner) return null;
  return <UserSettings />;
}

export function Integrations() {
  const t = useTranslations("settings");
  const user = useSession().data?.user;
  if (!user) return null;
  return <section aria-labelledby="integrations-heading" className="flex min-w-0 flex-col gap-3">
    <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
      <h2 id="integrations-heading" className="font-mono text-[0.6875rem] font-medium tracking-wider text-faint uppercase">{t("integrations")}</h2>
      <p className="text-xs text-muted-foreground">{t("integrationsHint")}</p>
    </div>
    <IntegrationSettings email={user.email} owner={user.owner} />
  </section>;
}
