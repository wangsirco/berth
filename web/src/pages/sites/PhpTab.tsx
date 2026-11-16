import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { useHost } from '@/components/shell/host'
import { Box, Dot, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select } from '@/components/ui/Field'
import { can, useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { useEvents } from '@/lib/useEvents'

interface Runtime { version: string; name: string; installed: boolean; running: boolean; extensions: string[]; ini: Record<string, string>; sites: string[] }
interface Data { items: Runtime[]; extensions: string[]; default_ext: string[]; ini_defaults: Record<string, string> }

// PHP runtimes (方案 §5.2.5): one php-fpm container per version, shared by the sites that pick it.
export function PhpTab() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'sites.manage')
  const d = useApi<Data>(`/api/v1/hosts/${host}/php`, [host])
  useEvents(['change.done', 'task.update'], () => void d.reload())
  const [edit, setEdit] = useState<Runtime | null>(null)
  if (!d.data) return d.error ? <Notice tone="bad">{d.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  return (
    <>
      <NativePhp />
      <Box className="mt-4" title={t('php.container_title')} right={<span className="text-[12px] text-mute">{t('php.box_hint')}</span>}>
        {d.data.items.map((r) => (
          <div key={r.version} className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-2.5 text-[13px] last:border-b-0">
            <Dot state={!r.installed ? 'off' : r.running ? 'ok' : 'bad'} />
            <b className="w-20">PHP {r.version}</b>
            <div className="min-w-0 flex-1">
              {r.installed ? <>
                <div className="flex flex-wrap gap-1">{r.extensions.map((e) => <Pill key={e}>{e}</Pill>)}</div>
                <div className="mt-0.5 text-[12px] text-mute">{r.sites.length ? t('php.used_by', { list: r.sites.join(', ') }) : t('php.unused')} · memory_limit {r.ini.memory_limit} · upload {r.ini.upload_max_filesize}</div>
              </> : <span className="text-mute">{t('php.not_installed')}</span>}
            </div>
            {manage && (r.installed ? <>
              <Button size="sm" onClick={() => setEdit(r)}>{t('php.settings')}</Button>
              <Button size="sm" variant="ghost" disabled={r.sites.length > 0} onClick={() => run('php.remove', { version: r.version }, { host })}>{t('action.php.remove')}</Button>
            </> : <Button size="sm" variant="primary" onClick={() => setEdit(r)}>{t('php.install')}</Button>)}
          </div>
        ))}
      </Box>
      {edit && <PhpDialog rt={edit} data={d.data} onClose={() => setEdit(null)} />}
    </>
  )
}

function PhpDialog({ rt, data, onClose }: { rt: Runtime; data: Data; onClose: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [ext, setExt] = useState<Set<string>>(new Set(rt.installed ? rt.extensions : data.default_ext))
  const [ini, setIni] = useState<Record<string, string>>({ ...data.ini_defaults, ...rt.ini })
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={(rt.installed ? t('php.settings') : t('php.install')) + ' · PHP ' + rt.version} wide
      footer={<><Button onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" onClick={() => run('php.set', { version: rt.version, extensions: [...ext], ini }, { host, onDone: onClose })}>{t('common.continue')}</Button></>}>
      <div className="space-y-3">
        <Field label={t('php.extensions')} hint={t('php.ext_hint')}>
          <div className="flex flex-wrap gap-x-4 gap-y-1.5 text-[13px]">
            {data.extensions.map((e) => <label key={e} className="flex items-center gap-1.5"><input type="checkbox" checked={ext.has(e)} onChange={(x) => { const n = new Set(ext); if (x.target.checked) n.add(e); else n.delete(e); setExt(n) }} /><span className="num">{e}</span></label>)}
          </div>
        </Field>
        <div className="grid gap-3 sm:grid-cols-2">
          {['memory_limit', 'upload_max_filesize', 'post_max_size', 'max_execution_time', 'max_input_vars'].map((k) => (
            <Field key={k} label={t('php.ini_' + k)}><Input className="num" value={ini[k] ?? ''} onChange={(e) => setIni({ ...ini, [k]: e.target.value })} /></Field>
          ))}
          <Field label={t('php.ini_display_errors')}>
            <Select value={ini.display_errors ?? 'Off'} onChange={(e) => setIni({ ...ini, display_errors: e.target.value })}><option value="Off">{t('php.off')}</option><option value="On">{t('php.on')}</option></Select>
          </Field>
        </div>
      </div>
    </Dialog>
  )
}

interface NativeRt { version: string; installed: boolean; extensions: string[]; sites: string[] }

// Native PHP: versions installed on the host (Remi / sury), each site running its own sandboxed FPM.
function NativePhp() {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'sites.manage')
  const d = useApi<{ items: NativeRt[] }>(`/api/v1/hosts/${host}/php/native`, [host])
  useEvents(['change.done', 'task.update'], () => void d.reload())
  return (
    <Box title={t('php.native_title')} right={<Pill tone="acc">{t('php.recommended')}</Pill>}>
      <p className="border-b border-line px-4 py-2 text-[12.5px] text-mute">{t('php.native_hint')}</p>
      {d.error && <div className="p-3"><Notice tone="warn">{d.error.message}</Notice></div>}
      {(d.data?.items ?? []).slice().reverse().map((r) => (
        <div key={r.version} className="flex flex-wrap items-center gap-3 border-b border-line px-4 py-2.5 text-[13px] last:border-b-0">
          <Dot state={r.installed ? 'ok' : 'off'} />
          <b className="w-20">PHP {r.version}</b>
          <div className="min-w-0 flex-1">
            {r.installed ? <>
              <div className="flex flex-wrap gap-1">{r.extensions.filter((e) => !['core', 'date', 'standard', 'spl', 'reflection', 'pcre', 'hash', 'random', 'libxml', 'filter'].includes(e)).slice(0, 18).map((e) => <Pill key={e}>{e}</Pill>)}</div>
              <div className="mt-0.5 text-[12px] text-mute">{r.sites.length ? t('php.used_by', { list: r.sites.join(', ') }) : t('php.unused')}</div>
            </> : <span className="text-mute">{t('php.not_installed')}</span>}
          </div>
          {manage && (r.installed
            ? <Button size="sm" variant="ghost" disabled={r.sites.length > 0} onClick={() => run('php.native.remove', { version: r.version }, { host })}>{t('action.php.native.remove')}</Button>
            : <Button size="sm" variant="primary" onClick={() => run('php.native.install', { version: r.version }, { host })}>{t('php.install')}</Button>)}
        </div>
      ))}
    </Box>
  )
}
