import type { Metadata, Viewport } from "next";
import type { ReactNode } from "react";
import { GeistMono } from "geist/font/mono";
import { GeistSans } from "geist/font/sans";
import { AppShell } from "@/components/app-shell";
import { Providers } from "@/components/providers";
import { appDescription, appTitle } from "@/i18n/metadata";
import { themeColors, themeScript } from "@/lib/theme";
import "./globals.css";

export const metadata: Metadata = {
  title: appTitle,
  description: appDescription,
};

export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: themeColors.light },
    { media: "(prefers-color-scheme: dark)", color: themeColors.dark },
  ],
  colorScheme: "light dark",
  viewportFit: "cover",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    // The inline script adds the dark class before the first paint, so React
    // finds a class it did not render. The provider updates lang when the
    // owner picks another language.
    <html lang="sk" className={`${GeistSans.variable} ${GeistMono.variable}`} suppressHydrationWarning>
      <head>
        <script dangerouslySetInnerHTML={{ __html: themeScript }} />
      </head>
      <body className="min-h-svh text-sm antialiased">
        <Providers><AppShell>{children}</AppShell></Providers>
      </body>
    </html>
  );
}
