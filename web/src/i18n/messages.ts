// Messages are split by namespace, one file per area of the app. Slovak is
// the reference: English must provide every key it has.
import type { Locale } from "@/lib/locale";
import sk_accounts from "./sk/accounts.json";
import sk_app from "./sk/app.json";
import sk_auth from "./sk/auth.json";
import sk_catalog from "./sk/catalog.json";
import sk_chat from "./sk/chat.json";
import sk_commands from "./sk/commands.json";
import sk_common from "./sk/common.json";
import sk_dashboard from "./sk/dashboard.json";
import sk_editor from "./sk/editor.json";
import sk_imports from "./sk/imports.json";
import sk_integrations from "./sk/integrations.json";
import sk_investing from "./sk/investing.json";
import sk_notifications from "./sk/notifications.json";
import sk_pages from "./sk/pages.json";
import sk_scheduledImports from "./sk/scheduledImports.json";
import sk_settings from "./sk/settings.json";
import sk_shell from "./sk/shell.json";
import sk_shortcuts from "./sk/shortcuts.json";
import sk_subscriptions from "./sk/subscriptions.json";
import sk_transactions from "./sk/transactions.json";
import sk_ui from "./sk/ui.json";
import en_accounts from "./en/accounts.json";
import en_app from "./en/app.json";
import en_auth from "./en/auth.json";
import en_catalog from "./en/catalog.json";
import en_chat from "./en/chat.json";
import en_commands from "./en/commands.json";
import en_common from "./en/common.json";
import en_dashboard from "./en/dashboard.json";
import en_editor from "./en/editor.json";
import en_imports from "./en/imports.json";
import en_integrations from "./en/integrations.json";
import en_investing from "./en/investing.json";
import en_notifications from "./en/notifications.json";
import en_pages from "./en/pages.json";
import en_scheduledImports from "./en/scheduledImports.json";
import en_settings from "./en/settings.json";
import en_shell from "./en/shell.json";
import en_shortcuts from "./en/shortcuts.json";
import en_subscriptions from "./en/subscriptions.json";
import en_transactions from "./en/transactions.json";
import en_ui from "./en/ui.json";

const sk = {
  accounts: sk_accounts,
  app: sk_app,
  auth: sk_auth,
  catalog: sk_catalog,
  chat: sk_chat,
  commands: sk_commands,
  common: sk_common,
  dashboard: sk_dashboard,
  editor: sk_editor,
  imports: sk_imports,
  integrations: sk_integrations,
  investing: sk_investing,
  notifications: sk_notifications,
  pages: sk_pages,
  scheduledImports: sk_scheduledImports,
  settings: sk_settings,
  shell: sk_shell,
  shortcuts: sk_shortcuts,
  subscriptions: sk_subscriptions,
  transactions: sk_transactions,
  ui: sk_ui,
};

export type Messages = typeof sk;

export const messages: Record<Locale, Messages> = {
  sk,
  en: {
    accounts: en_accounts,
    app: en_app,
    auth: en_auth,
    catalog: en_catalog,
    chat: en_chat,
    commands: en_commands,
    common: en_common,
    dashboard: en_dashboard,
    editor: en_editor,
    imports: en_imports,
    integrations: en_integrations,
    investing: en_investing,
    notifications: en_notifications,
    pages: en_pages,
    scheduledImports: en_scheduledImports,
    settings: en_settings,
    shell: en_shell,
    shortcuts: en_shortcuts,
    subscriptions: en_subscriptions,
    transactions: en_transactions,
    ui: en_ui,
  },
};
