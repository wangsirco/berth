import { ArrowRight, Ban, Bell, Check, Copy, Download, ExternalLink, Eye, Gauge, KeyRound, Network, Router, ShieldCheck, Waypoints } from 'lucide-react'
import { useState } from 'react'
import { GitHubMark as Github } from '@/components/brand/GitHubMark'
import { WharfMark } from '@/components/brand/WharfMark'
import { Box, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { cx } from '@/components/ui/cx'
import { useAuth } from '@/lib/auth'
import { useApi } from '@/lib/hooks'
import { useT } from '@/lib/i18n'
import { ConnBadge, LiveLine } from '../links/LinksPage'
import { useEvents } from '@/lib/useEvents'
import { WharfPair, WharfPaired, type WharfStatus } from '../network/NetworkPage'
import { PortCheck } from './PortCheck'

const REPO = 'https://github.com/wangsirco/wharf'
const INSTALL = 'opkg install /tmp/luci-app-wharf_*.ipk'

// Wharf (码头), Berth's sister product for OpenWrt routers. Paired: its view from here. Not yet:
// what it is, where to get it, and pairing step by step.
export default function WharfPage() {
  const t = useT()
  const st = useApi<WharfStatus>('/api/v1/wharf')
  useEvents(['change.done'], () => void st.reload())
  if (!st.data) return st.error ? <Notice tone="bad">{st.error.message}</Notice> : <div className="p-6 text-[13px] text-mute">{t('common.loading')}</div>
  if (st.data.paired && st.data.link) {
    return (
      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-3 rounded-[var(--radius-box)] border border-line bg-card px-4 py-3">
          <WharfMark className="size-8" />
          <div className="min-w-0 flex-1"><div className="text-[14px] font-semibold">{t('wharfp.paired_with', { name: st.data.link.name })}</div><div className="num truncate text-[12px] text-mute">{st.data.link.url}</div>
            <div className="mt-1 flex flex-wrap items-center gap-2"><ConnBadge live={st.data.live} /><LiveLine live={st.data.live} /></div></div>
          <a href={st.data.link.url} target="_blank" rel="noreferrer"><Button size="sm" variant="primary"><ExternalLink className="size-3.5" />{t('wharf.open')}</Button></a>
          <a href={REPO} target="_blank" rel="noreferrer"><Button size="sm" variant="ghost"><Github className="size-3.5" />GitHub</Button></a>
        </div>
        <WharfPaired st={st.data} reload={() => void st.reload()} />
        <PortCheck />
      </div>
    )
  }
  return <Intro berthFP={st.data.berth_fp} />
}

function Intro({ berthFP }: { berthFP: string }) {
  const t = useT()
  const { user } = useAuth()
  const [copied, setCopied] = useState(false)
  const features = [
    { Icon: Eye, k: 'see' }, { Icon: KeyRound, k: 'control' }, { Icon: Waypoints, k: 'diagnose' }, { Icon: Network, k: 'build' },
  ]
  const together = [
    { Icon: Router, k: 'forward' }, { Icon: Ban, k: 'bans' }, { Icon: Bell, k: 'alerts' }, { Icon: ShieldCheck, k: 'sso' }, { Icon: Gauge, k: 'traffic' }, { Icon: Download, k: 'ddns' },
  ]
  return (
    <div className="mx-auto max-w-5xl space-y-6 pb-12">
      {/* what it is */}
      <section className="overflow-hidden rounded-[calc(var(--radius-box)+4px)] border border-line bg-card">
        <div className="grid gap-6 p-6 md:grid-cols-[1.1fr_1fr] md:p-8">
          <div className="flex flex-col justify-center">
            <div className="flex items-center gap-3">
              <WharfMark className="size-12" />
              <div>
                <div className="text-[24px] font-bold leading-tight">Wharf <span className="font-semibold text-mute">码头</span></div>
                <Pill tone="acc">{t('wharfp.sister')}</Pill>
              </div>
            </div>
            <p className="mt-4 text-[16px] font-semibold leading-snug">{t('wharfp.tagline')}</p>
            <p className="mt-2 text-[13.5px] leading-relaxed text-mute">{t('wharfp.lead')}</p>
            <div className="mt-5 flex flex-wrap gap-2">
              <a href={REPO} target="_blank" rel="noreferrer"><Button variant="primary"><Github className="size-4" />{t('wharfp.repo')}</Button></a>
              <a href={REPO + '/releases/latest'} target="_blank" rel="noreferrer"><Button><Download className="size-4" />{t('wharfp.download')}</Button></a>
              <a href="#pair"><Button variant="ghost">{t('wharfp.have_it')}<ArrowRight className="size-4" /></Button></a>
            </div>
            <p className="num mt-3 text-[12px] text-mute">{REPO.replace('https://', '')}</p>
          </div>
          <img src="/wharf/dashboard.jpg" alt={t('wharfp.shot_alt')} className="w-full rounded-xl border border-line shadow-sm" loading="lazy" />
        </div>
      </section>

      {/* highlights */}
      <section className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        {features.map(({ Icon, k }) => (
          <div key={k} className="rounded-[var(--radius-box)] border border-line bg-card p-4">
            <Icon className="size-5 text-[#2563eb]" />
            <div className="mt-2 text-[14px] font-semibold">{t('wharfp.f_' + k)}</div>
            <p className="mt-1 text-[12.5px] leading-relaxed text-mute">{t('wharfp.f_' + k + '_d')}</p>
          </div>
        ))}
      </section>

      {/* with Berth */}
      <Box title={t('wharfp.together')}>
        <div className="grid gap-x-6 gap-y-3 px-4 py-4 sm:grid-cols-2">
          {together.map(({ Icon, k }) => (
            <div key={k} className="flex gap-2.5">
              <Icon className="mt-0.5 size-4 shrink-0 text-acc" />
              <div><div className="text-[13.5px] font-semibold">{t('wharfp.t_' + k)}</div><div className="text-[12.5px] text-mute">{t('wharfp.t_' + k + '_d')}</div></div>
            </div>
          ))}
        </div>
      </Box>

      {/* pairing, step by step */}
      <section id="pair" className="scroll-mt-4 space-y-3">
        <h2 className="text-[16px] font-semibold">{t('wharfp.pair_title')}</h2>
        <Step n={1} title={t('wharfp.s1')}>
          <p className="text-[12.5px] text-mute">{t('wharfp.s1_d')}</p>
          <div className="mt-2 flex items-center gap-2 rounded-lg bg-term px-3 py-2">
            <code className="num flex-1 text-[12.5px] text-term-fg">{INSTALL}</code>
            <button type="button" className="text-term-fg/70 hover:text-term-fg" aria-label={t('common.copy')}
              onClick={() => { void navigator.clipboard?.writeText(INSTALL); setCopied(true); setTimeout(() => setCopied(false), 1500) }}>
              {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
            </button>
          </div>
          <p className="mt-2 text-[12px] text-mute">{t('wharfp.s1_req')}</p>
        </Step>
        <Step n={2} title={t('wharfp.s2')}>
          <p className="mb-2 text-[12.5px] text-mute">{t('wharfp.s2_d')}</p>
          {user?.role === 'super' ? <WharfPair berthFP={berthFP} bare /> : <Notice>{t('wharf.super_only')}</Notice>}
        </Step>
        <Step n={3} title={t('wharfp.s3')} last>
          <p className="text-[12.5px] text-mute">{t('wharfp.s3_d')}</p>
        </Step>
      </section>
    </div>
  )
}

function Step({ n, title, children, last }: { n: number; title: string; children: React.ReactNode; last?: boolean }) {
  return (
    <div className="flex gap-3">
      <div className="flex flex-col items-center">
        <span className="grid size-7 shrink-0 place-items-center rounded-full bg-[#2563eb] text-[13px] font-semibold text-white">{n}</span>
        {!last && <span className="mt-1 w-px flex-1 bg-line" />}
      </div>
      <div className={cx('min-w-0 flex-1 rounded-[var(--radius-box)] border border-line bg-card px-4 py-3', !last && 'mb-1')}>
        <div className="mb-1 text-[14px] font-semibold">{title}</div>
        {children}
      </div>
    </div>
  )
}
