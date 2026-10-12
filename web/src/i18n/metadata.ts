import app from "@/i18n/sk/app.json";

// The static export renders page metadata once, in the default language. The
// pages rename the browser tab when the owner picks another language.
export function pageTitle(page: keyof typeof app.pages): string {
  return app.pages[page];
}

export const appTitle = { default: app.title.replace("{page}", app.pages.overview), template: app.title.replace("{page}", "%s") };
export const appDescription = app.description;
