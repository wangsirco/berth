import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'

export type Theme = 'system' | 'light' | 'dark'
const Ctx = createContext<{ theme: Theme; setTheme: (t: Theme) => void; dark: boolean }>({ theme: 'system', setTheme: () => {}, dark: false })

export function ThemeProvider({ children }: { children: ReactNode }) {
  const [theme, setTheme] = useState<Theme>(() => (localStorage.getItem('berth.theme') as Theme) || 'system')
  const [sysDark, setSysDark] = useState(() => matchMedia('(prefers-color-scheme: dark)').matches)
  useEffect(() => {
    const mq = matchMedia('(prefers-color-scheme: dark)')
    const on = () => setSysDark(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  const dark = theme === 'dark' || (theme === 'system' && sysDark)
  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark)
    localStorage.setItem('berth.theme', theme)
  }, [dark, theme])
  return <Ctx.Provider value={{ theme, setTheme, dark }}>{children}</Ctx.Provider>
}

export const useTheme = () => useContext(Ctx)
