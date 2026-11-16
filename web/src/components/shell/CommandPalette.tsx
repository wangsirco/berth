import { Dialog as D } from '@base-ui/react/dialog'
import { useEffect, useMemo, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'
import type { Container } from '@/lib/types'
import { useChange } from '../change/ChangeDialog'
import { cx } from '../ui/cx'
import { hostPath, useHost } from './host'
import { ALL } from './nav'

interface Entry { id: string; label: string; hint: string; go: () => void }

// ⌘K: pages and objects; a query starting with ">" lists actions ("> 重启 n8n").
export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useT()
  const nav = useNavigate()
  const run = useChange()
  const { user } = useAuth()
  const { host } = useHost()
  const [q, setQ] = useState('')
  const [idx, setIdx] = useState(0)
  const [containers, setContainers] = useState<Container[]>([])
  const [kinds, setKinds] = useState<string[]>([])
  useEffect(() => {
    if (!open) return
    setQ('')
    setIdx(0)
    if (can(user, 'containers.view')) get<{ items: Container[] }>(`/api/v1/hosts/${host}/containers`).then((r) => setContainers(r.items)).catch(() => {})
    get<{ items: { name: string }[] }>('/api/v1/kinds').then((r) => setKinds(r.items.map((k) => k.name))).catch(() => {})
  }, [open, host, user])
  const entries = useMemo<Entry[]>(() => {
    const close = (fn: () => void) => () => { onOpenChange(false); fn() }
    const action = q.trimStart().startsWith('>')
    const needle = q.replace(/^\s*>/, '').trim().toLowerCase()
    const all: Entry[] = []
    if (action) {
      for (const c of containers) {
        for (const k of ['container.restart', 'container.stop', 'container.start']) {
          if (!kinds.includes(k)) continue
          if (k === 'container.start' && c.state === 'running') continue
          if (k !== 'container.start' && c.state !== 'running') continue
          all.push({ id: k + c.name, label: `${t('action.' + k)} ${c.name}`, hint: c.image, go: close(() => run(k, { name: c.name })) })
        }
      }
    } else {
      for (const n of ALL.filter((x) => !x.perm || can(user, x.perm))) {
        all.push({ id: 'p' + n.key, label: t('nav.' + n.key), hint: t('palette.page'), go: close(() => nav(hostPath(host, n.path))) })
      }
      all.push({ id: 'acct', label: t('shell.account'), hint: t('palette.page'), go: close(() => nav('/account')) })
      for (const c of containers) all.push({ id: 'c' + c.name, label: c.name, hint: t('obj.container') + ' · ' + c.image, go: close(() => nav(hostPath(host, 'containers/c/' + encodeURIComponent(c.name)))) })
    }
    return all.filter((e) => !needle || (e.label + ' ' + e.hint).toLowerCase().includes(needle)).slice(0, 50)
  }, [q, containers, kinds, t, nav, run, host, user, onOpenChange])
  useEffect(() => setIdx(0), [q])
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Backdrop className="fixed inset-0 z-40 bg-black/25" />
        <D.Popup className="fixed left-1/2 top-[12vh] z-50 w-[calc(100%-1.5rem)] max-w-xl -translate-x-1/2 overflow-hidden rounded-[var(--radius-dialog)] border border-line bg-canvas shadow-[0_8px_24px_rgba(66,74,83,.18)] outline-none">
          <D.Title className="sr-only">{t('shell.search')}</D.Title>
          <input autoFocus value={q} onChange={(e) => setQ(e.target.value)} placeholder={t('palette.placeholder')}
            onKeyDown={(e) => {
              if (e.key === 'ArrowDown') { e.preventDefault(); setIdx((i) => Math.min(i + 1, entries.length - 1)) }
              if (e.key === 'ArrowUp') { e.preventDefault(); setIdx((i) => Math.max(i - 1, 0)) }
              if (e.key === 'Enter') entries[idx]?.go()
            }}
            className="w-full border-b border-line bg-transparent px-4 py-3 text-[15px] outline-none" />
          <ul role="listbox" className="max-h-[50vh] overflow-auto py-1">
            {entries.map((e, i) => (
              <li key={e.id} role="option" aria-selected={i === idx} onMouseEnter={() => setIdx(i)} onClick={e.go}
                className={cx('flex cursor-pointer items-center gap-3 px-4 py-2 text-[13.5px]', i === idx && 'bg-tint')}>
                <span className="truncate">{e.label}</span>
                <span className="ml-auto truncate text-[12px] text-mute">{e.hint}</span>
              </li>
            ))}
            {entries.length === 0 && <li className="px-4 py-6 text-center text-[13px] text-mute">{t('palette.nothing')}</li>}
          </ul>
          <div className="border-t border-line px-4 py-1.5 text-[11.5px] text-mute">{t('palette.hint')}</div>
        </D.Popup>
      </D.Portal>
    </D.Root>
  )
}
