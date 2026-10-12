"use client";

import { useMutation } from "@tanstack/react-query";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { useDocumentTitle } from "@/components/locale";
import { signIn } from "@/lib/api";
import { showServerErrors } from "@/lib/forms";
import { useSetSession } from "@/lib/session";
import { alertClass, alertDescriptionClass, footerClass, legendClass } from "@/components/panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Field, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Kbd } from "@/components/ui/kbd";
import { Turnstile } from "@/components/turnstile";

type Values = { email: string; password: string };

export function SignInForm() {
  const t = useTranslations("auth");
  const app = useTranslations("app");
  useDocumentTitle(app("pages.login"));
  const setSession = useSetSession();
  const [message, setMessage] = useState("");
  const [turnstileToken, setTurnstileToken] = useState("");
  const [challenge, setChallenge] = useState(0);
  const form = useForm<Values>({ defaultValues: { email: "", password: "" } });
  const { errors } = form.formState;
  const mutation = useMutation({
    mutationFn: signIn,
    // The shell then returns the user to the page that asked them to sign in.
    onSuccess: setSession,
    onError: (error) => {
      setTurnstileToken("");
      setChallenge((value) => value + 1);
      form.resetField("password");
      setMessage(showServerErrors(error, form.setError, ["email", "password"]));
    },
  });
  return <div className="mx-auto flex w-full max-w-sm flex-col gap-4">
    <div className="flex flex-col gap-1">
      <p className={legendClass}>{t("workspace")}</p>
      <h1 className="text-xl font-semibold tracking-tight">{t("signIn")}</h1>
      <p className="text-sm text-muted-foreground">{t("signInHint")}</p>
    </div>
    <Card size="sm" className="gap-0 py-0">
      <CardContent className="px-3.5 py-4">
        <form noValidate aria-label={t("signIn")} onSubmit={form.handleSubmit((values) => {
          if (!turnstileToken || mutation.isPending) return;
          setMessage("");
          mutation.mutate({ ...values, turnstileToken });
        })}
          className="flex flex-col gap-4">
          {message && <Alert variant="destructive" className={alertClass}>
            <AlertDescription role="alert" className={alertDescriptionClass}>{message}</AlertDescription>
          </Alert>}
          <FieldGroup className="gap-4">
            <Field data-invalid={Boolean(errors.email)} className="gap-1.5">
              <FieldLabel htmlFor="email" className="text-xs text-muted-foreground">{t("email")}</FieldLabel>
              <Input id="email" type="email" autoComplete="username" autoFocus aria-invalid={Boolean(errors.email)}
                aria-describedby={errors.email ? "email-error" : undefined}
                {...form.register("email", { required: t("enterEmail") })} />
              {errors.email && <FieldError id="email-error" className="text-xs">{errors.email.message}</FieldError>}
            </Field>
            <Field data-invalid={Boolean(errors.password)} className="gap-1.5">
              <FieldLabel htmlFor="password" className="text-xs text-muted-foreground">{t("password")}</FieldLabel>
              <Input id="password" type="password" autoComplete="current-password" aria-invalid={Boolean(errors.password)}
                aria-describedby={errors.password ? "password-error" : undefined}
                {...form.register("password", { required: t("enterPassword") })} />
              {errors.password && <FieldError id="password-error" className="text-xs">{errors.password.message}</FieldError>}
            </Field>
          </FieldGroup>
          <Turnstile key={challenge} onTokenChange={setTurnstileToken} />
          <Button type="submit" disabled={mutation.isPending || !turnstileToken}>
            {mutation.isPending ? t("signingIn") : t("signIn")}
            <Kbd aria-hidden="true" className="border-primary-foreground/20 bg-primary-foreground/10 text-primary-foreground max-md:hidden">Enter</Kbd>
          </Button>
        </form>
      </CardContent>
      <CardFooter className={footerClass}>{t("signInFooter")}</CardFooter>
    </Card>
  </div>;
}
