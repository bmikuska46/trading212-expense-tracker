import { createTranslator } from "next-intl";
import { messages } from "@/i18n/messages";
import { currentLocale } from "@/lib/locale";

// translator reads messages outside of React, such as in the API client, in
// the language the provider last rendered.
export function translator() {
  const locale = currentLocale();
  return createTranslator({ locale, messages: messages[locale] });
}
