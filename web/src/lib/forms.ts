import type { FieldValues, Path, UseFormSetError } from "react-hook-form";
import { ApiError } from "@/lib/api";
import { translator } from "@/i18n/translate";

// showServerErrors moves field messages from a 422 response onto the form and
// returns the message to show above it.
export function showServerErrors<T extends FieldValues>(error: unknown, setError: UseFormSetError<T>, fields: readonly Path<T>[]): string {
  if (!(error instanceof ApiError)) return translator()("common.errors.generic");
  let focused = false;
  for (const field of fields) {
    const message = error.fields[field];
    if (message) {
      setError(field, { type: "server", message }, { shouldFocus: !focused });
      focused = true;
    }
  }
  return error.message;
}
