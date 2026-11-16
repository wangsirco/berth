import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { useEffect, useImperativeHandle, useRef, forwardRef } from 'react'
import { useTheme } from '@/lib/theme'

export interface TerminalHandle { send: (s: string) => void; focus: () => void; selection: () => string }

const themes = {
  light: { background: '#ffffff', foreground: '#1f2328', cursor: '#0b6e66', selectionBackground: '#0b6e6633', black: '#24292f', blue: '#0969da', cyan: '#1b7c83', green: '#116329', magenta: '#8250df', red: '#cf222e', white: '#6e7781', yellow: '#4d2d00' },
  dark: { background: '#010409', foreground: '#d1d9e0', cursor: '#2bb3a6', selectionBackground: '#2bb3a644', black: '#484f58', blue: '#58a6ff', cyan: '#39c5cf', green: '#3fb950', magenta: '#bc8cff', red: '#ff7b72', white: '#b1bac4', yellow: '#d29922' },
}

// TerminalView attaches xterm.js to a session's WebSocket. Binary frames carry terminal
// bytes; text frames carry {"t":"resize"} control messages.
export const TerminalView = forwardRef<TerminalHandle, { url: string; onExit?: () => void }>(function TerminalView({ url, onExit }, ref) {
  const box = useRef<HTMLDivElement>(null)
  const term = useRef<Terminal>(null)
  const ws = useRef<WebSocket>(null)
  const { dark } = useTheme()
  const exit = useRef(onExit)
  exit.current = onExit
  useImperativeHandle(ref, () => ({
    send: (s) => { if (ws.current?.readyState === 1) ws.current.send(new TextEncoder().encode(s)) },
    focus: () => term.current?.focus(),
    selection: () => term.current?.getSelection() ?? '',
  }))
  useEffect(() => {
    const t = new Terminal({ fontFamily: '"JetBrains Mono", ui-monospace, monospace', fontSize: 13, cursorBlink: true, scrollback: 5000, theme: dark ? themes.dark : themes.light, allowProposedApi: false })
    const fit = new FitAddon()
    t.loadAddon(fit)
    t.loadAddon(new WebLinksAddon())
    t.open(box.current!)
    term.current = t
    const sock = new WebSocket(url)
    sock.binaryType = 'arraybuffer'
    ws.current = sock
    const resize = () => {
      try { fit.fit() } catch { return }
      if (sock.readyState === 1) sock.send(JSON.stringify({ t: 'resize', cols: t.cols, rows: t.rows }))
    }
    sock.onopen = () => { resize(); t.focus() }
    sock.onmessage = (e) => t.write(typeof e.data === 'string' ? e.data : new Uint8Array(e.data))
    sock.onclose = () => { t.write('\r\n\x1b[90m[disconnected]\x1b[0m\r\n'); exit.current?.() }
    const enc = new TextEncoder()
    const d = t.onData((s) => { if (sock.readyState === 1) sock.send(enc.encode(s)) })
    const ro = new ResizeObserver(() => resize())
    ro.observe(box.current!)
    return () => { ro.disconnect(); d.dispose(); sock.close(); t.dispose() }
    // theme changes are applied below without reconnecting
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [url])
  useEffect(() => { if (term.current) term.current.options.theme = dark ? themes.dark : themes.light }, [dark])
  return <div ref={box} className="h-full w-full overflow-hidden bg-[#ffffff] px-1 pt-1 dark:bg-[#010409]" />
})
