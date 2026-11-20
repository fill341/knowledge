// Theme preference ("system" | "light" | "dark") and the language/theme controls.
// The theme is applied as data-theme on <html> only when it is not "system"; styles.css holds the palettes.
import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import { LANG_NAMES, useT, type LangPref } from "./i18n";

export type ThemePref = "system" | "light" | "dark";

const THEME_KEY = "kn.theme";

function readTheme(): ThemePref {
  try {
    const v = localStorage.getItem(THEME_KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    /* storage unavailable: follow the system */
  }
  return "system";
}

function applyTheme(p: ThemePref) {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  if (p === "system") root.removeAttribute("data-theme");
  else root.setAttribute("data-theme", p);
}

// Apply before the first render so there is no flash of the wrong palette.
applyTheme(readTheme());

interface ThemeState {
  theme: ThemePref;
  setTheme: (p: ThemePref) => void;
}

const ThemeCtx = createContext<ThemeState>({ theme: "system", setTheme: () => {} });

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setThemeState] = useState<ThemePref>(readTheme);
  const setTheme = useCallback((p: ThemePref) => {
    try {
      if (p === "system") localStorage.removeItem(THEME_KEY);
      else localStorage.setItem(THEME_KEY, p);
    } catch {
      /* private mode: the choice just is not remembered */
    }
    applyTheme(p);
    setThemeState(p);
  }, []);
  return <ThemeCtx.Provider value={{ theme, setTheme }}>{children}</ThemeCtx.Provider>;
}

export const useTheme = () => useContext(ThemeCtx);

// Compact language and theme selects, used in the sidebar footer and on the login page.
export function PrefsControls({ className = "" }: { className?: string }) {
  const { t, pref, setLang } = useT();
  const { theme, setTheme } = useTheme();
  return (
    <div className={`prefs ${className}`}>
      <label>
        <span>{t("prefs.language")}</span>
        <select value={pref} onChange={(e) => setLang(e.target.value as LangPref)} aria-label={t("prefs.language")}>
          <option value="system">{t("prefs.system")}</option>
          <option value="en">{LANG_NAMES.en}</option>
          <option value="ru">{LANG_NAMES.ru}</option>
        </select>
      </label>
      <label>
        <span>{t("prefs.theme")}</span>
        <select value={theme} onChange={(e) => setTheme(e.target.value as ThemePref)} aria-label={t("prefs.theme")}>
          <option value="system">{t("prefs.system")}</option>
          <option value="light">{t("prefs.light")}</option>
          <option value="dark">{t("prefs.dark")}</option>
        </select>
      </label>
    </div>
  );
}
