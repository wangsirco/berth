import { ResourceFit } from '@/components/ResourceFit'
import { AlertTriangle, BadgeCheck, Box as BoxIcon, Cpu, ShieldAlert, ShieldQuestion } from 'lucide-react'
import { useState } from 'react'
import { useChange } from '@/components/change/ChangeDialog'
import { Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog } from '@/components/ui/Dialog'
import { Field, Input, Select, Switch } from '@/components/ui/Field'
import { cx } from '@/components/ui/cx'
import { bytes } from '@/lib/format'
import { useLocale, useT } from '@/lib/i18n'

type Text = Record<string, string>
export interface PSetting { key: string; label: Text; hint?: Text; type: 'text' | 'number' | 'bool' | 'select' | 'secret'; default?: string; options?: string[]; required?: boolean }
export interface PManifest {
  api: number; id: string; name: Text; version: string; description: Text; author: string; homepage?: string
  runtime: { type: 'container' | 'native'; image?: string; build?: string; port?: number; binary?: Record<string, string>; run_as?: 'plugin' | 'root'; mem_mb?: number; cpus?: number; page?: boolean }
  system: { listen?: number[]; firewall_open?: boolean; capabilities?: string[]; read_paths?: string[]; write_paths?: string[]; host_network?: boolean; packages?: string[] }
  permissions: string[]; events?: string[]; settings?: PSetting[]
}
export interface Inspect {
  ref: string; manifest: PManifest; size: number; signed: boolean; sig_ok: boolean; fingerprint?: string; pinned_fingerprint?: string; key_changed: boolean
  installed_version?: string; more_scopes?: string[]; arch_ok: boolean; icon?: string; screenshots?: string[]; from_market: boolean; root_blocked?: string
}

export const tx = (x: Text | undefined, locale: string) => x?.[locale] || x?.en || x?.['zh-CN'] || ''

// scopeText: "sites:write" in words.
export function scopeText(t: ReturnType<typeof useT>, s: string) {
  const [res, act] = s.split(':')
  return `${t('res.' + res)} · ${t('tokens.scope_' + act)}`
}

export function PluginIcon({ src, size = 40 }: { src?: string; size?: number }) {
  return src ? <img src={src} alt="" width={size} height={size} className="shrink-0 rounded-[10px]" />
    : <span className="grid shrink-0 place-items-center rounded-[10px] bg-tint text-acc" style={{ width: size, height: size }}><BoxIcon className="size-1/2" /></span>
}

// InstallWizard: 介绍 → 权限 → 设置 → 确认 (the change preview). Nothing happens before the last step.
export function InstallWizard({ info, onClose, onDone }: { info: Inspect; onClose: () => void; onDone: (r: Record<string, unknown>) => void }) {
  const t = useT()
  const run = useChange()
  const [locale] = useLocale()
  const m = { ...info.manifest, permissions: info.manifest.permissions ?? [], system: info.manifest.system ?? {} }
  const root = m.runtime.type === 'native' && m.runtime.run_as === 'root'
  const settings = m.settings ?? []
  const steps = ['intro', 'perms', ...(settings.length ? ['settings'] : []), 'confirm'] as const
  const [i, setI] = useState(0)
  const step = steps[i]
  const [grant, setGrant] = useState<string[]>(m.permissions)
  const [agreeSystem, setAgreeSystem] = useState(false)
  const [vals, setVals] = useState<Record<string, string>>(() => Object.fromEntries(settings.map((s) => [s.key, s.default ?? (s.type === 'bool' ? 'false' : '')])))
  const sys = m.system ?? {}
  const sysLines: { text: string; danger?: boolean }[] = [
    ...(root ? [{ text: t('plugin.sys_root'), danger: true }] : m.runtime.type === 'native' ? [{ text: t('plugin.sys_user', { user: 'berth-p-' + m.id }) }] : [{ text: t('plugin.sys_container') }]),
    ...(sys.packages?.length ? [{ text: t('plugin.sys_packages', { list: sys.packages.join(' ') }) }] : []),
    ...(sys.listen ?? []).map((p) => ({ text: t(sys.firewall_open ? 'plugin.sys_listen_open' : 'plugin.sys_listen', { port: p }) })),
    ...(sys.capabilities ?? []).map((c) => ({ text: t('plugin.cap_' + c), danger: c === 'net_admin' })),
    ...(sys.write_paths ?? []).map((p) => ({ text: t('plugin.sys_write', { path: p }) })),
    ...(sys.read_paths ?? []).map((p) => ({ text: t('plugin.sys_read', { path: p }) })),
    ...(sys.host_network ? [{ text: t('plugin.sys_host_network'), danger: true }] : []),
    ...(m.events?.length ? [{ text: t('plugin.sys_events', { list: m.events.join(', ') }) }] : []),
  ]
  const missing = settings.some((s) => s.required && !vals[s.key])
  const blocked = !!info.root_blocked || !info.arch_ok
  const next = () => setI(i + 1)
  const install = () => run('plugin.install', { ref: info.ref, grant, settings: vals }, { host: 'local', typeToConfirm: root ? m.id : undefined, onDone: (c) => { if (c.status === 'done') onDone((c.result ?? {}) as Record<string, unknown>) } })
  const sig = !info.signed ? { tone: 'warn' as const, Icon: ShieldQuestion, text: t('plugin.unsigned') }
    : !info.sig_ok ? { tone: 'bad' as const, Icon: ShieldAlert, text: t('plugin.sig_bad') }
      : info.key_changed ? { tone: 'bad' as const, Icon: ShieldAlert, text: t('plugin.key_changed', { old: info.pinned_fingerprint ?? '', now: info.fingerprint ?? '' }) }
        : { tone: 'ok' as const, Icon: BadgeCheck, text: t('plugin.signed', { fp: info.fingerprint ?? '' }) }
  return (
    <Dialog open wide onOpenChange={(o) => !o && onClose()} title={info.installed_version ? t('plugin.update_title', { name: tx(m.name, locale) }) : t('plugin.install_title', { name: tx(m.name, locale) })}
      footer={<>
        <ol className="mr-auto flex items-center gap-1.5 text-[12px] text-mute">
          {steps.map((s, k) => <li key={s} className={cx('rounded-full px-2 py-0.5', k === i ? 'bg-tint font-semibold text-acc' : k < i ? 'text-fg' : '')}>{k + 1}. {t('plugin.step_' + s)}</li>)}
        </ol>
        {i > 0 && <Button onClick={() => setI(i - 1)}>{t('common.back')}</Button>}
        {step !== 'confirm'
          ? <Button variant="primary" disabled={(step === 'intro' && blocked) || (step === 'perms' && !agreeSystem) || (step === 'settings' && missing)} onClick={next}>{t('common.continue')}</Button>
          : <Button variant={root ? 'danger' : 'primary'} onClick={install}>{info.installed_version ? t('plugin.do_update') : t('plugin.do_install')}</Button>}
      </>}>
      <div className="space-y-4 px-4 py-3">
        {step === 'intro' && <>
          <div className="flex gap-3">
            <PluginIcon src={info.icon} size={56} />
            <div className="min-w-0 flex-1">
              <div className="text-[16px] font-semibold">{tx(m.name, locale)} <span className="num text-[13px] font-normal text-mute">{m.version}</span></div>
              <div className="text-[12.5px] text-mute">{t('plugin.by', { author: m.author })}{m.homepage && <> · <a className="text-acc hover:underline" href={m.homepage} target="_blank" rel="noreferrer">{t('plugin.homepage')}</a></>} · {bytes(info.size)}</div>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                <Pill tone="acc">{m.runtime.type === 'container' ? t('plugin.rt_container') : root ? t('plugin.rt_root') : t('plugin.rt_native')}</Pill>
                {info.from_market && <Pill>{t('plugin.from_market')}</Pill>}
                {info.installed_version && <Pill>{t('plugin.installed_v', { v: info.installed_version })}</Pill>}
              </div>
            </div>
          </div>
          <p className="text-[13.5px] leading-relaxed">{tx(m.description, locale)}</p>
          <ResourceFit cores={m.runtime.cpus && m.runtime.cpus >= 1 ? Math.ceil(m.runtime.cpus) : undefined} memMB={(m.runtime.mem_mb ?? 256) + 1024} runMB={m.runtime.mem_mb ?? 256}
            diskMB={Math.max(50, Math.round(info.size / 2 ** 20) * (m.runtime.type === 'container' ? 3 : 1))} impacts={[m.runtime.type === 'container' ? 'fit.plugin_container' : 'fit.plugin_native', 'fit.plugin_run']} />
          {info.screenshots && info.screenshots.length > 0 && <div className="flex gap-2 overflow-x-auto pb-1">{info.screenshots.map((s, k) => <img key={k} src={s} alt="" className="h-40 rounded-lg border border-line" />)}</div>}
          <div className={cx('flex items-start gap-2 rounded-xl border px-3 py-2 text-[12.5px]', sig.tone === 'ok' ? 'border-ok/40 text-ok' : sig.tone === 'warn' ? 'border-warn/50 text-warn' : 'border-bad/50 text-bad')}>
            <sig.Icon className="mt-0.5 size-4 shrink-0" /><span>{sig.text}</span>
          </div>
          {!info.arch_ok && <Notice tone="bad">{t('plugin.no_arch')}</Notice>}
          {info.root_blocked && <Notice tone="bad">{t(info.root_blocked)}</Notice>}
        </>}

        {step === 'perms' && <>
          {root && (
            <div className="flex gap-3 rounded-xl border-2 border-bad bg-[color-mix(in_srgb,var(--bad)_8%,transparent)] px-3.5 py-3">
              <AlertTriangle className="mt-0.5 size-5 shrink-0 text-bad" />
              <div className="text-[13px]"><b className="text-bad">{t('plugin.root_title')}</b><p className="mt-0.5">{t('plugin.root_body')}</p></div>
            </div>
          )}
          <div>
            <h3 className="mb-1.5 text-[13.5px] font-semibold">{t('plugin.perms_berth')}</h3>
            {m.permissions.length === 0 ? <p className="text-[12.5px] text-mute">{t('plugin.perms_none')}</p> : (
              <div className="space-y-1">
                <p className="text-[12px] text-mute">{t('plugin.perms_berth_hint')}</p>
                {m.permissions.map((s) => (
                  <label key={s} className="flex items-center gap-2 rounded-lg border border-line px-2.5 py-1.5 text-[13px]">
                    <input type="checkbox" checked={grant.includes(s)} onChange={(e) => setGrant(e.target.checked ? [...grant, s] : grant.filter((x) => x !== s))} />
                    <span className="flex-1">{scopeText(t, s)}</span>
                    {s.endsWith(':write') && <Pill tone="warn">{t('plugin.can_change')}</Pill>}
                    {info.more_scopes?.includes(s) && <Pill tone="acc">{t('plugin.new_scope')}</Pill>}
                  </label>
                ))}
              </div>
            )}
          </div>
          <div>
            <h3 className="mb-1.5 text-[13.5px] font-semibold">{t('plugin.perms_system')}</h3>
            <ul className="space-y-1">
              {sysLines.map((l, k) => (
                <li key={k} className={cx('flex items-start gap-2 rounded-lg border px-2.5 py-1.5 text-[13px]', l.danger ? 'border-bad/50 text-bad' : 'border-line')}>
                  <Cpu className="mt-0.5 size-3.5 shrink-0 text-mute" />{l.text}
                </li>
              ))}
            </ul>
            <div className="mt-2.5"><Switch checked={agreeSystem} onChange={setAgreeSystem} label={t(root ? 'plugin.agree_root' : 'plugin.agree_system')} /></div>
          </div>
        </>}

        {step === 'settings' && settings.map((s) => (
          <Field key={s.key} label={tx(s.label, locale) + (s.required ? ' *' : '')} hint={tx(s.hint, locale)}>
            {s.type === 'bool' ? <Switch checked={vals[s.key] === 'true'} onChange={(x) => setVals({ ...vals, [s.key]: x ? 'true' : 'false' })} label={tx(s.label, locale)} />
              : s.type === 'select' ? <Select value={vals[s.key]} onChange={(e) => setVals({ ...vals, [s.key]: e.target.value })}>{s.options!.map((o) => <option key={o}>{o}</option>)}</Select>
                : <Input type={s.type === 'secret' ? 'password' : s.type === 'number' ? 'number' : 'text'} value={vals[s.key]} onChange={(e) => setVals({ ...vals, [s.key]: e.target.value })} aria-label={tx(s.label, locale)} />}
          </Field>
        ))}

        {step === 'confirm' && <>
          <p className="text-[13.5px]">{t('plugin.confirm_line', { name: tx(m.name, locale), n: grant.length, total: m.permissions.length })}</p>
          <p className="text-[12.5px] text-mute">{t('plugin.confirm_hint')}</p>
          {root && <Notice tone="bad">{t('plugin.confirm_root', { id: m.id })}</Notice>}
        </>}
      </div>
    </Dialog>
  )
}
