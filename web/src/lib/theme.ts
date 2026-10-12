// Theme is the owner's choice. System follows the operating system and is
// the default until the owner picks one. The root layout reads this module on
// the server; the hooks live in components/theme.tsx.
export type Theme = "light" | "dark" | "system";

// Theme names live in the app.theme messages under the same keys.
export const themes: Theme[] = ["light", "dark", "system"];

export const themeStorageKey = "theme";
export const darkQuery = "(prefers-color-scheme: dark)";

// Browser chrome matches the page background of each palette.
export const themeColors = { light: "#f7f6f1", dark: "#0b0d0f" };

// themeScript runs in <head> while the page parses, so the saved theme
// applies before the first paint. It mirrors applyTheme in components/theme.tsx.
export const themeScript = `(function(){try{var t=localStorage.getItem("${themeStorageKey}");var d=t==="dark"||(t!=="light"&&matchMedia("${darkQuery}").matches);document.documentElement.classList.toggle("dark",d)}catch(e){}})()`;
