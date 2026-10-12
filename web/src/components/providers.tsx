"use client";

import { MutationCache, QueryCache, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { HotkeysProvider } from "@tanstack/react-hotkeys";
import { NextIntlClientProvider } from "next-intl";
import { Fragment, useLayoutEffect, useState, type ReactNode } from "react";
import { ApiError } from "@/lib/api";
import { defaultTimezone } from "@/lib/format";
import { setFormatLocale } from "@/lib/locale";
import { messages } from "@/i18n/messages";
import { useLocaleSetting } from "@/components/locale";
import { useThemeSync } from "@/components/theme";
import { TooltipProvider } from "@/components/ui/tooltip";

// maxQueries bounds the cached query results kept in memory.
export const maxQueries = 100;

export function Providers({ children }: { children: ReactNode }) {
  useThemeSync();
  const locale = useLocaleSetting();
  setFormatLocale(locale);
  useLayoutEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);
  const [client] = useState(() => {
    // An expired session makes any request fail with 401. Reloading the
    // session sends the owner to sign in again.
    const onError = (error: Error) => {
      if (error instanceof ApiError && error.status === 401) void client.invalidateQueries({ queryKey: ["session"] });
    };
    const client: QueryClient = new QueryClient({
      queryCache: new QueryCache({ onError }),
      mutationCache: new MutationCache({ onError }),
      defaultOptions: {
        queries: {
          staleTime: 30_000, gcTime: 300_000,
          retry: (count, error) => !(error instanceof ApiError && error.status >= 400 && error.status < 500) && count < 1,
        },
      },
    });
    // Each filter, page and month is its own query. Past the limit, the
    // least recently updated queries that nothing on screen uses are dropped.
    const cache = client.getQueryCache();
    cache.subscribe((event) => {
      if (event.type !== "added") return;
      const idle = cache.getAll().filter((query) => !query.getObserversCount());
      const excess = cache.getAll().length - maxQueries;
      if (excess <= 0) return;
      idle.sort((a, b) => a.state.dataUpdatedAt - b.state.dataUpdatedAt)
        .slice(0, excess).forEach((query) => cache.remove(query));
    });
    return client;
  });
  // Changing the language remounts the pages, so values formatted outside
  // of React render again too. Cached queries are kept.
  return (
    <QueryClientProvider client={client}>
      <NextIntlClientProvider locale={locale} messages={messages[locale]} timeZone={defaultTimezone}>
        <HotkeysProvider defaultOptions={{ hotkey: { ignoreInputs: true } }}>
          <TooltipProvider delayDuration={300}><Fragment key={locale}>{children}</Fragment></TooltipProvider>
        </HotkeysProvider>
      </NextIntlClientProvider>
    </QueryClientProvider>
  );
}
