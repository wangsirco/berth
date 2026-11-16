import { useState } from 'react'
import { Box, Notice, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Textarea } from '@/components/ui/Field'
import { del, errText, post } from '@/lib/api'
import { datetime } from '@/lib/format'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'

// Personal SSH public keys: new machines, dev containers and workspaces get them.
export function SshKeys() {
  const t = useT()
  const keys = useApi<{ items: { id: number; name: string; key: string; created_at: number }[] }>('/api/v1/me/ssh-keys')
  const [text, setText] = useState('')
  const [err, setErr] = useState('')
  const add = async () => {
    setErr('')
    try { await post('/api/v1/me/ssh-keys', { key: text }); setText(''); void keys.reload() } catch (e) { setErr(errText(e)) }
  }
  return (
    <Box title={t('account.ssh_keys')} count={keys.data?.items.length}>
      <p className="px-4 pt-3 text-[12px] text-mute">{t('account.ssh_keys_hint')}</p>
      {keys.data?.items.length === 0 && <p className="px-4 pt-2 text-[12.5px] text-mute">{t('account.no_ssh_keys')}</p>}
      {keys.data?.items.map((k) => (
        <Row key={k.id}>
          <div className="min-w-0 flex-1"><div className="text-[13px] font-semibold">{k.name}</div><div className="num truncate text-[11.5px] text-mute">{k.key} · {datetime(k.created_at)}</div></div>
          <Button size="sm" variant="ghost" onClick={async () => { await del(`/api/v1/me/ssh-keys/${k.id}`); void keys.reload() }}>{t('common.delete')}</Button>
        </Row>
      ))}
      <div className="space-y-2 px-4 py-3">
        {err && <Notice tone="bad">{err}</Notice>}
        <Textarea rows={2} className="num text-[12px]" value={text} placeholder={t('account.ssh_key_paste')} onChange={(e) => setText(e.target.value)} aria-label={t('account.ssh_key_paste')} />
        <Button size="sm" disabled={!text.trim()} onClick={() => void add()}>{t('account.ssh_key_add')}</Button>
      </div>
    </Box>
  )
}
