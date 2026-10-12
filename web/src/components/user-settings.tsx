"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useForm } from "react-hook-form";
import { useTranslations } from "next-intl";
import { KeyRound, MoreHorizontal, Trash2 } from "lucide-react";
import { createUser, deleteUser, resetUserPassword, listUsers, type UserEntry } from "@/lib/api";
import { formatDateTime } from "@/lib/format";
import { showServerErrors } from "@/lib/forms";
import { alertClass, alertDescriptionClass, cellClass, compactFields, footerClass, headClass, PanelHeader } from "@/components/panel";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardFooter } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { cn } from "@/lib/utils";

type Values = { email: string; password: string };

function useTemporaryPassword() {
  const t = useTranslations("auth");
  return {
    required: t("choosePassword"),
    minLength: { value: 12, message: t("passwordMin") },
    maxLength: { value: 256, message: t("passwordMax") },
  };
}

function AddUserForm({ report }: { report: (text: string) => void }) {
  const t = useTranslations("settings.users");
  const auth = useTranslations("auth");
  const client = useQueryClient();
  const rules = useTemporaryPassword();
  const [message, setMessage] = useState("");
  const form = useForm<Values>({ defaultValues: { email: "", password: "" } });
  const { errors } = form.formState;
  const mutation = useMutation({
    mutationFn: createUser,
    onSuccess: (created) => {
      client.setQueryData<UserEntry[]>(["users"], (current) => current && [...current, created]);
      form.reset();
      report(t("added", { email: created.email }));
    },
    onError: (error) => setMessage(showServerErrors(error, form.setError, ["email", "password"])),
  });
  return <form noValidate aria-label={t("add")} className={cn("flex flex-col gap-3", compactFields)}
    onSubmit={form.handleSubmit((values) => { setMessage(""); report(""); mutation.mutate(values); })}>
    {message && <Alert variant="destructive" className={alertClass}>
      <AlertDescription role="alert" className={alertDescriptionClass}>{message}</AlertDescription>
    </Alert>}
    <FieldGroup className="grid gap-3 sm:grid-cols-2">
      <Field data-invalid={Boolean(errors.email)}>
        <FieldLabel htmlFor="new-user-email">{t("email")}</FieldLabel>
        <Input id="new-user-email" type="email" autoComplete="off" className="h-8 text-[0.8125rem]" aria-invalid={Boolean(errors.email)}
          aria-describedby={errors.email ? "new-user-email-error" : undefined}
          {...form.register("email", { required: auth("enterEmail") })} />
        {errors.email && <FieldError id="new-user-email-error">{errors.email.message}</FieldError>}
      </Field>
      <Field data-invalid={Boolean(errors.password)}>
        <FieldLabel htmlFor="new-user-password">{t("temporaryPassword")}</FieldLabel>
        <Input id="new-user-password" type="text" autoComplete="off" spellCheck={false} className="h-8 font-mono text-[0.8125rem]"
          aria-invalid={Boolean(errors.password)}
          aria-describedby={`new-user-password-description${errors.password ? " new-user-password-error" : ""}`}
          {...form.register("password", rules)} />
        {errors.password && <FieldError id="new-user-password-error">{errors.password.message}</FieldError>}
      </Field>
      <FieldDescription id="new-user-password-description" className="sm:col-span-2">{t("temporaryHint")}</FieldDescription>
    </FieldGroup>
    <Button type="submit" size="sm" className="w-fit" disabled={mutation.isPending}>{mutation.isPending ? t("adding") : t("add")}</Button>
  </form>;
}

function ResetDialog({ user, onClose, report }: { user: UserEntry; onClose: () => void; report: (text: string) => void }) {
  const t = useTranslations("settings.users");
  const common = useTranslations("common");
  const client = useQueryClient();
  const rules = useTemporaryPassword();
  const [message, setMessage] = useState("");
  const form = useForm<{ password: string }>({ defaultValues: { password: "" } });
  const error = form.formState.errors.password;
  const mutation = useMutation({
    mutationFn: ({ password }: { password: string }) => resetUserPassword(user.id, password),
    onSuccess: (updated) => {
      client.setQueryData<UserEntry[]>(["users"], (current) => current?.map((item) => item.id === updated.id ? updated : item));
      report(t("resetDone", { email: updated.email }));
      onClose();
    },
    onError: (failure) => setMessage(showServerErrors(failure, form.setError, ["password"])),
  });
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{t("resetTitle", { email: user.email })}</DialogTitle>
        <DialogDescription>{t("resetDescription")}</DialogDescription>
      </DialogHeader>
      <form noValidate onSubmit={form.handleSubmit((values) => { setMessage(""); mutation.mutate(values); })}
        className={cn("flex flex-col gap-3", compactFields)}>
        {message && <p role="alert" className="text-xs text-destructive">{message}</p>}
        <Field data-invalid={Boolean(error)}>
          <FieldLabel htmlFor="reset-password">{t("temporaryPassword")}</FieldLabel>
          <Input id="reset-password" type="text" autoComplete="off" spellCheck={false} autoFocus className="font-mono"
            aria-invalid={Boolean(error)} aria-describedby={error ? "reset-password-error" : undefined} {...form.register("password", rules)} />
          {error && <FieldError id="reset-password-error">{error.message}</FieldError>}
        </Field>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>{common("cancel")}</Button>
          <Button type="submit" disabled={mutation.isPending}>{mutation.isPending ? t("resetting") : t("reset")}</Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>;
}

function DeleteDialog({ user, onClose, report }: { user: UserEntry; onClose: () => void; report: (text: string) => void }) {
  const t = useTranslations("settings.users");
  const common = useTranslations("common");
  const client = useQueryClient();
  const remove = useMutation({
    mutationFn: () => deleteUser(user.id),
    onSuccess: () => {
      client.setQueryData<UserEntry[]>(["users"], (current) => current?.filter((item) => item.id !== user.id));
      report(t("deleted", { email: user.email }));
      onClose();
    },
  });
  return <Dialog open onOpenChange={(open) => { if (!open) onClose(); }}>
    <DialogContent className="sm:max-w-md">
      <DialogHeader>
        <DialogTitle>{t("deleteTitle", { email: user.email })}</DialogTitle>
        <DialogDescription>{t("deleteDescription")}</DialogDescription>
      </DialogHeader>
      {remove.isError && <p role="alert" className="text-xs text-destructive">{remove.error.message}</p>}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>{common("cancel")}</Button>
        <Button type="button" variant="destructive" disabled={remove.isPending} onClick={() => remove.mutate()}>
          {remove.isPending ? t("deleting") : t("delete")}
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>;
}

// UserSettings lets the owner add users, give them a new temporary password
// and delete them together with their ledger.
export function UserSettings() {
  const t = useTranslations("settings.users");
  const query = useQuery({ queryKey: ["users"], queryFn: listUsers });
  const [status, setStatus] = useState("");
  const [resetting, setResetting] = useState<UserEntry | null>(null);
  const [deleting, setDeleting] = useState<UserEntry | null>(null);
  return <Card size="sm" className="gap-0 py-0">
    <PanelHeader id="users-heading" title={t("title")} count={query.data?.length} />
    <CardContent className="flex flex-col gap-3 px-0 py-0">
      <p className="px-3.5 pt-3 text-xs text-muted-foreground">{t("intro")}</p>
      {query.isPending ? <div role="status" aria-label={t("loading")} className="px-3.5"><Skeleton className="h-16" /></div>
        : query.isError ? <p role="alert" className="px-3.5 text-sm text-destructive">{query.error.message}</p>
        : <Table>
          <TableCaption className="sr-only">{t("caption")}</TableCaption>
          <TableHeader>
            <TableRow>
              <TableHead className={headClass}>{t("email")}</TableHead>
              <TableHead className={cn(headClass, "max-sm:hidden")}>{t("created")}</TableHead>
              <TableHead className={headClass}><span className="sr-only">{t("actionsColumn")}</span></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {query.data.map((user) => <TableRow key={user.id}>
              <TableCell className={cn(cellClass, "max-w-56")}>
                <span className="flex min-w-0 items-center gap-2">
                  <span className="truncate font-mono text-xs">{user.email}</span>
                  {user.owner ? <Badge variant="secondary">{t("you")}</Badge>
                    : user.mustChangePassword && <Badge variant="outline">{t("pending")}</Badge>}
                </span>
              </TableCell>
              <TableCell className={cn(cellClass, "font-mono text-xs text-muted-foreground max-sm:hidden")}>{formatDateTime(user.createdAt)}</TableCell>
              <TableCell className={cn(cellClass, "w-10 text-right")}>
                {!user.owner && <DropdownMenu>
                  <DropdownMenuTrigger asChild>
                    <Button variant="ghost" size="icon-sm" aria-label={t("actions", { email: user.email })}><MoreHorizontal aria-hidden="true" /></Button>
                  </DropdownMenuTrigger>
                  <DropdownMenuContent align="end">
                    <DropdownMenuItem onSelect={() => setResetting(user)}><KeyRound aria-hidden="true" />{t("reset")}</DropdownMenuItem>
                    <DropdownMenuItem variant="destructive" onSelect={() => setDeleting(user)}><Trash2 aria-hidden="true" />{t("delete")}</DropdownMenuItem>
                  </DropdownMenuContent>
                </DropdownMenu>}
              </TableCell>
            </TableRow>)}
          </TableBody>
        </Table>}
    </CardContent>
    <CardFooter className={cn(footerClass, "flex-col items-stretch gap-2 py-3")}>
      <AddUserForm report={setStatus} />
      <p aria-live="polite" className="text-xs text-muted-foreground empty:hidden">{status}</p>
    </CardFooter>
    {resetting && <ResetDialog user={resetting} onClose={() => setResetting(null)} report={setStatus} />}
    {deleting && <DeleteDialog user={deleting} onClose={() => setDeleting(null)} report={setStatus} />}
  </Card>;
}
