import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import zhCN from '../i18n/zh-CN.json'
import zhTW from '../i18n/zh-TW.json'
import en from '../i18n/en.json'
import { setApiLocale } from './api'

export type Locale = 'zh-CN' | 'zh-TW' | 'en'
export const LOCALES: Locale[] = ['zh-CN', 'zh-TW', 'en']
const dicts: Record<Locale, Record<string, string>> = { 'zh-CN': zhCN, 'zh-TW': zhTW, en }

// pickLocale: the user's setting wins, then the browser language.
export function pickLocale(user: string, nav: string): Locale {
  if (LOCALES.includes(user as Locale)) return user as Locale
  const t = nav.toLowerCase()
  if (/^zh-(tw|hk|mo)|^zh-hant/.test(t)) return 'zh-TW'
  if (t.startsWith('zh')) return 'zh-CN'
  return 'en'
}

export type TFn = (key: string, params?: Record<string, unknown>) => string

export function translate(locale: Locale, key: string, params?: Record<string, unknown>) {
  let s = dicts[locale][key] ?? dicts.en[key]
  if (s === undefined) {
    if (import.meta.env.DEV) console.warn('missing i18n key', key)
    s = key
  }
  if (params) for (const [k, v] of Object.entries(params)) s = s.replaceAll('{' + k + '}', String(v))
  return s
}

const Ctx = createContext<{ locale: Locale; setLocale: (l: Locale) => void; t: TFn }>({
  locale: 'zh-CN', setLocale: () => {}, t: (k) => k,
})

export function I18nProvider({ initial, children }: { initial?: Locale; children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() =>
    initial ?? pickLocale(localStorage.getItem('berth.locale') ?? '', navigator.language))
  const setLocale = useCallback((l: Locale) => {
    setLocaleState(l)
    localStorage.setItem('berth.locale', l)
  }, [])
  useEffect(() => {
    setApiLocale(locale)
    document.documentElement.lang = locale
  }, [locale])
  const t = useCallback<TFn>((k, p) => translate(locale, k, p), [locale])
  const v = useMemo(() => ({ locale, setLocale, t }), [locale, setLocale, t])
  return <Ctx.Provider value={v}>{children}</Ctx.Provider>
}

export const useT = () => useContext(Ctx).t
export const useLocale = () => {
  const c = useContext(Ctx)
  return [c.locale, c.setLocale] as const
}

// pick chooses this locale's string from a server-translated map ({zh-CN, zh-TW, en}).
export function usePick() {
  const { locale } = useContext(Ctx)
  return useCallback((m?: Record<string, string>) => (m ? m[locale] ?? m.en ?? '' : ''), [locale])
}
