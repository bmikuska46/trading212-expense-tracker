"use client";

import Script from "next/script";
import { useCallback, useRef, useState } from "react";
import { useLocale, useTranslations } from "next-intl";
import { useTheme } from "@/components/theme";

type TurnstileAPI = {
  render: (container: HTMLElement, options: {
    sitekey: string;
    action: string;
    theme: "light" | "dark";
    language: string;
    size: "flexible" | "compact";
    "response-field": false;
    callback: (token: string) => void;
    "expired-callback": () => void;
    "timeout-callback": () => void;
    "error-callback": () => void;
  }) => string;
  remove: (widget: string) => void;
};

declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}

const siteKey = process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY ?? "1x00000000000000000000AA";

export function Turnstile({ onTokenChange }: { onTokenChange: (token: string) => void }) {
  const t = useTranslations("auth");
  const locale = useLocale();
  const { resolved } = useTheme();
  const container = useRef<HTMLDivElement>(null);
  const widget = useRef<string | null>(null);
  const compact = useRef(false);
  const [failed, setFailed] = useState(false);

  const fail = useCallback(() => {
    onTokenChange("");
    setFailed(true);
  }, [onTokenChange]);

  const render = useCallback(() => {
    const api = window.turnstile;
    if (!api || !container.current || widget.current !== null) return;
    onTokenChange("");
    try {
      compact.current = container.current.clientWidth < 300;
      widget.current = api.render(container.current, {
        sitekey: siteKey,
        action: "login",
        theme: resolved,
        language: locale,
        size: compact.current ? "compact" : "flexible",
        "response-field": false,
        callback: (token) => { setFailed(false); onTokenChange(token); },
        "expired-callback": () => onTokenChange(""),
        "timeout-callback": () => onTokenChange(""),
        "error-callback": fail,
      });
    } catch {
      fail();
    }
  }, [fail, locale, onTokenChange, resolved]);

  const attach = useCallback((node: HTMLDivElement) => {
    container.current = node;
    render();
    const observer = new ResizeObserver(() => {
      if (widget.current === null || compact.current === (node.clientWidth < 300)) return;
      window.turnstile?.remove(widget.current);
      widget.current = null;
      render();
    });
    observer.observe(node);
    return () => {
      observer.disconnect();
      if (widget.current !== null) window.turnstile?.remove(widget.current);
      widget.current = null;
      container.current = null;
      onTokenChange("");
    };
  }, [onTokenChange, render]);

  return <div className="flex flex-col gap-1.5">
    <div ref={attach} className="min-h-[65px]" aria-label={t("securityVerification")} />
    {failed && <p role="alert" className="text-xs text-destructive">{t("verificationUnavailable")}</p>}
    <Script src="https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit"
      onReady={render} onError={fail} />
  </div>;
}
