import { Archive } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { hostPath } from '@/components/shell/host'
import { Box, Notice } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Field, Input } from '@/components/ui/Field'
import { download, errText } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { ReauthProvider, useReauth } from '../account/reauth'

// 面板快照：数据库、密钥、证书打成一个用密码加密的文件，换机器时 `berth restore-panel` 还原。
export function PanelSnapshot() {
  return <ReauthProvider><Inner /></ReauthProvider>
}

function Inner() {
  const t = useT()
  const reauth = useReauth()
  const [pw, setPw] = useState('')
  const [pw2, setPw2] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null)
  const ok = pw.length >= 8 && pw === pw2
  const go = async () => {
    setBusy(true); setMsg(null)
    try {
      await reauth(() => download('/api/v1/panel/snapshot', { password: pw }, 'berth.berthsnap'))
      setMsg({ ok: true, text: t('snapshot.done') }); setPw(''); setPw2('')
    } catch (e) { setMsg({ ok: false, text: errText(e) }) } finally { setBusy(false) }
  }
  return (
    <Box title={t('snapshot.title')}>
      <p className="px-4 pt-3 text-[12.5px] text-mute">{t('snapshot.hint')}</p>
      {msg && <div className="px-4 pt-2"><Notice tone={msg.ok ? 'acc' : 'bad'}>{msg.text}</Notice></div>}
      <Field label={t('snapshot.password')} hint={t('snapshot.password_hint')}><Input type="password" autoComplete="new-password" value={pw} onChange={(e) => setPw(e.target.value)} /></Field>
      <Field label={t('snapshot.password2')}><Input type="password" autoComplete="new-password" value={pw2} onChange={(e) => setPw2(e.target.value)} /></Field>
      <div className="px-4 pb-3">
        <Button variant="primary" disabled={!ok || busy} onClick={() => void go()}><Archive className="size-3.5" />{busy ? t('snapshot.working') : t('snapshot.download')}</Button>
        <p className="mt-2 text-[12px] text-mute">{t('snapshot.restore_hint')} <code className="num">sudo berth restore-panel berth-….berthsnap</code></p>
        <p className="mt-1 text-[12px] text-mute">{t('snapshot.scheduled_hint')} <Link className="text-acc hover:underline" to={hostPath('local', 'backup')}>{t('snapshot.to_backup')}</Link></p>
      </div>
    </Box>
  )
}
