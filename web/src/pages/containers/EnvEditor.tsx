import { Eye, EyeOff, Plus, X } from 'lucide-react'
import { useState } from 'react'
import { Button } from '@/components/ui/Button'
import { Input } from '@/components/ui/Field'
import { useT } from '@/lib/i18n'

const SECRET = /(PASS|SECRET|TOKEN|KEY|PRIVATE)/i

type Line = { kind: 'kv'; key: string; value: string } | { kind: 'raw'; text: string }

export function parseEnv(text: string): Line[] {
  const lines = text.split('\n')
  if (lines.length && lines[lines.length - 1] === '') lines.pop()
  return lines.map((l) => {
    const m = l.match(/^([A-Za-z_][A-Za-z0-9_.-]*)=(.*)$/)
    return m ? { kind: 'kv', key: m[1], value: m[2] } : { kind: 'raw', text: l }
  })
}

export const formatEnv = (lines: Line[]) => (lines.length ? lines.map((l) => (l.kind === 'kv' ? `${l.key}=${l.value}` : l.text)).join('\n') + '\n' : '')

// EnvEditor: a key/value table over .env text. Comments, blank lines and order survive;
// secret-looking values are masked until revealed (they are on the page anyway, since
// editing needs them; the server only sends them to people with containers.manage).
export function EnvEditor({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const t = useT()
  const lines = parseEnv(value)
  const [shown, setShown] = useState<Record<number, boolean>>({})
  const update = (i: number, patch: Partial<{ key: string; value: string }>) => {
    const next = lines.map((l, j) => (j === i && l.kind === 'kv' ? { ...l, ...patch } : l))
    onChange(formatEnv(next))
  }
  return (
    <div className="space-y-1.5">
      {lines.map((l, i) => l.kind === 'raw' ? (
        l.text.trim() ? <div key={i} className="num truncate px-1 text-[12px] text-mute">{l.text}</div> : null
      ) : (
        <div key={i} className="flex items-center gap-1.5">
          <Input className="num h-7 w-[38%] text-[12.5px]" value={l.key} onChange={(e) => update(i, { key: e.target.value })} aria-label={t('env.key')} />
          <span className="text-mute">=</span>
          {SECRET.test(l.key) && !shown[i] ? (
            <span data-masked className="num flex h-7 flex-1 items-center rounded-[var(--radius-box)] border border-line bg-soft px-2.5 text-[12.5px] text-mute">••••</span>
          ) : (
            <Input className="num h-7 flex-1 text-[12.5px]" value={l.value} onChange={(e) => update(i, { value: e.target.value })} aria-label={t('env.value', { key: l.key })} />
          )}
          {SECRET.test(l.key) && (
            <Button size="sm" variant="ghost" aria-label={shown[i] ? t('env.hide') : t('env.show')} onClick={() => setShown({ ...shown, [i]: !shown[i] })}>
              {shown[i] ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
            </Button>
          )}
          <Button size="sm" variant="ghost" aria-label={t('env.remove')} onClick={() => onChange(formatEnv(lines.filter((_, j) => j !== i)))}><X className="size-3.5" /></Button>
        </div>
      ))}
      <Button size="sm" onClick={() => onChange(formatEnv([...lines, { kind: 'kv', key: 'NEW_VAR', value: '' }]))}><Plus className="size-3.5" />{t('env.add')}</Button>
    </div>
  )
}
