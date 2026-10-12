import type { Messages } from "@/i18n/messages";
import type { Locale } from "@/lib/locale";

// Typed keys: a message that does not exist fails the type check.
declare module "next-intl" {
  interface AppConfig {
    Locale: Locale;
    Messages: Messages;
  }
}
