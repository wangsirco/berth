import { useEffect, useState } from 'react'
import { ObjectActivity } from '@/components/ai/Activity'
import { useApi } from '@/lib/hooks'
import { useNavigate } from 'react-router-dom'
import { DriftNotice } from '@/components/drift/DriftNotice'
import { LimitsBox } from './Upkeep'
import { useChange } from '@/components/change/ChangeDialog'
import { CodeEditor } from '@/components/code/CodeEditor'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Dot, Empty, Notice, Pill, Row } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Field, Input, Segmented } from '@/components/ui/Field'
import { ApiError, get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { useT } from '@/lib/i18n'
import { EnvEditor } from './EnvEditor'

export interface Project { name: string; dir: string; config_files: string[]; services: string[]; running: number; total: number; in_stacks: boolean; adopted: boolean }

export function ComposeList({ list, reload }: { list: Project[]; reload: () => void }) {
  const t = useT()
  const nav = useNavigate()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'containers.manage')
  const [creating, setCreating] = useState(false)
  const apps = useApi<{ items: { name: string; app_id: string }[] }>(`/api/v1/hosts/${host}/apps`, [host])
  const fromStore = new Map((apps.data?.items ?? []).map((a) => [a.name, a.app_id]))
  return (
    <>
      <Box title={t('containers.tab_apps')} count={list.length} right={manage && <Button size="sm" variant="primary" onClick={() => setCreating(true)}>{t('compose.new')}</Button>}>
        {list.length === 0 && <Empty title={t('compose.none')}>{t('compose.none_hint')}</Empty>}
        {list.map((p) => (
          <Row key={p.name} onClick={() => nav(hostPath(host, 'containers/compose/' + encodeURIComponent(p.name)))}>
            <Dot state={p.total === 0 ? 'off' : p.running === p.total ? 'ok' : 'warn'} />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-1.5 text-[13.5px] font-semibold">{p.name}
                {fromStore.has(p.name) ? <Pill tone="acc">{t('compose.from_store', { app: fromStore.get(p.name) ?? '' })}</Pill> : p.adopted && <Pill>{t('compose.own')}</Pill>}
                {!p.adopted && <Pill tone="warn">{t('compose.discovered')}</Pill>}</div>
              <div className="num truncate text-[12px] text-mute">{p.dir} · {p.services.join(', ') || '—'}</div>
            </div>
            <Pill className="num">{p.running}/{p.total}</Pill>
            {!p.adopted && manage && <Button size="sm" onClick={(e) => { e.stopPropagation(); run('compose.adopt', { name: p.name }, { host, onDone: reload }) }}>{t('action.compose.adopt')}</Button>}
          </Row>
        ))}
      </Box>
      {creating && <NewCompose onClose={() => setCreating(false)} onDone={reload} />}
    </>
  )
}

const SAMPLE = `services:
  app:
    image: nginx:alpine
    restart: unless-stopped
    ports:
      - "127.0.0.1:8080:80"
`

function NewCompose({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [name, setName] = useState('')
  const [compose, setCompose] = useState(SAMPLE)
  const [env, setEnv] = useState('')
  const [start, setStart] = useState(true)
  const create = () => {
    onClose()
    run('compose.create', { name, compose, env }, {
      host, onDone: () => { onDone(); if (start) run('compose.up', { name }, { host, onDone }) },
    })
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} wide title={t('compose.new')} description={t('compose.new_sub')}
      footer={<>
        <label className="flex items-center gap-2 text-[13px]"><input type="checkbox" checked={start} onChange={(e) => setStart(e.target.checked)} />{t('compose.start_after')}</label>
        <Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(name)} onClick={create}>{t('common.next')}</Button>
      </>}>
      <Field label={t('compose.name')} hint={t('compose.name_hint')}><Input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="myapp" autoFocus /></Field>
      <Field label="compose.yaml"><CodeEditor value={compose} onChange={setCompose} language="yaml" height="260px" /></Field>
      <Field label=".env" className="pb-3"><EnvEditor value={env} onChange={setEnv} /></Field>
    </Dialog>
  )
}

export function ComposeDrawer({ project, onClose, onChanged }: { project: Project; onClose: () => void; onChanged: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const manage = can(user, 'containers.manage')
  const [tab, setTab] = useState<'compose' | 'env'>('compose')
  const [files, setFiles] = useState<{ compose: string; env: string; file: string } | null>(null)
  const [draft, setDraft] = useState<{ compose: string; env: string } | null>(null)
  const [err, setErr] = useState('')
  const load = () => {
    if (!manage) return
    get<{ compose: string; env: string; file: string }>(`/api/v1/hosts/${host}/compose/${encodeURIComponent(project.name)}/files`)
      .then((f) => { setFiles(f); setDraft({ compose: f.compose, env: f.env }) }).catch((e: ApiError) => setErr(e.message))
  }
  useEffect(load, [project.name, host, manage])
  const dirty = files && draft && (files.compose !== draft.compose || files.env !== draft.env)
  const act = (kind: string, after?: () => void) => () => run(kind, { name: project.name }, { host, onDone: () => { onChanged(); after?.() } })
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={<>{project.name} {!project.adopted && <Pill tone="warn">{t('compose.discovered')}</Pill>}</>}
      right={manage && (
        <div className="flex flex-wrap gap-1.5">
          {!project.adopted && <Button size="sm" variant="primary" onClick={act('compose.adopt')}>{t('action.compose.adopt')}</Button>}
          <Button size="sm" onClick={act('compose.up')}>{t('action.compose.up')}</Button>
          <Button size="sm" onClick={act('compose.pull', () => { if (confirm(t('compose.recreate_now'))) act('compose.up')() })}>{t('action.compose.pull')}</Button>
          <Button size="sm" onClick={act('compose.restart')}>{t('action.compose.restart')}</Button>
          <Button size="sm" onClick={act('compose.down')}>{t('action.compose.down')}</Button>
          <Button size="sm" variant="danger" onClick={act('compose.remove', onClose)}>{t('action.compose.remove')}</Button>
        </div>
      )}
      footer={manage && draft && <>
        <span className="text-[12.5px] text-mute">{dirty ? t('compose.unsaved') : t('compose.saved_note')}</span>
        <Button className="ml-auto" disabled={!dirty} onClick={() => files && setDraft({ compose: files.compose, env: files.env })}>{t('compose.discard')}</Button>
        <Button variant="primary" disabled={!dirty} onClick={() => run('compose.update', { name: project.name, compose: draft.compose, env: draft.env }, { host, onDone: () => { load(); onChanged() } })}>{t('action.compose.update')}</Button>
      </>}>
      <div className="space-y-3 p-4">
        <ObjectActivity type="compose_project" id={project.name} />
        {err && <Notice tone="bad">{err}</Notice>}
        <DriftNotice host={host} kind="compose" object={project.name} />
        <div className="text-[13px]"><span className="text-mute">{t('compose.dir')}</span> <span className="num">{project.dir}</span></div>
        <div className="text-[13px]"><span className="text-mute">{t('compose.services')}</span> {project.services.join(', ') || '—'} · {project.running}/{project.total} {t('compose.running')}</div>
        {!manage && <Notice>{t('compose.view_only')}</Notice>}
        {manage && project.services.length > 0 && <LimitsBox project={project.name} services={project.services} />}
        {draft && (
          <>
            <Segmented value={tab} onChange={setTab} options={[{ value: 'compose', label: files?.file.split('/').pop() ?? 'compose.yaml' }, { value: 'env', label: '.env' }]} />
            {tab === 'compose' ? <CodeEditor value={draft.compose} onChange={(v) => setDraft({ ...draft, compose: v })} language="yaml" height="calc(100vh - 330px)" />
              : <EnvEditor value={draft.env} onChange={(v) => setDraft({ ...draft, env: v })} />}
          </>
        )}
      </div>
    </Drawer>
  )
}
