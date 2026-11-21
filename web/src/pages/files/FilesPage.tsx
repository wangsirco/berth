import { folderLabel } from './labels'
import { ChevronRight, Download, File, FileArchive, Folder, FolderPlus, FilePlus, Link2, MoreHorizontal, Search, Star, Trash2, Upload as UploadIcon } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useChange } from '@/components/change/ChangeDialog'
import { CodeEditor, languageOf } from '@/components/code/CodeEditor'
import { hostPath, useHost } from '@/components/shell/host'
import { Box, Empty, Notice, Pill } from '@/components/ui/Box'
import { Button } from '@/components/ui/Button'
import { Dialog, Drawer } from '@/components/ui/Dialog'
import { Field, Input, Switch } from '@/components/ui/Field'
import { Menu } from '@/components/ui/Menu'
import { cx } from '@/components/ui/cx'
import { ApiError, get } from '@/lib/api'
import { can, useAuth } from '@/lib/auth'
import { bytes, datetime } from '@/lib/format'
import { useT } from '@/lib/i18n'
import { AvScanButton } from './AvScan'
import { SftpButton } from './Sftp'
import { SyncButton } from './Sync'
import { ShareDialog } from './ShareDialog'
import { runQueue, uploadFile } from './Upload'

export interface Entry { name: string; path: string; dir: boolean; link?: boolean; link_target?: string; size: number; mtime: number; mode: string; perm: string; owner: string; group: string }

const join = (dir: string, name: string) => (dir === '/' ? '' : dir) + '/' + name
const parent = (p: string) => p.replace(/\/[^/]+\/?$/, '') || '/'
const ARCHIVE = /\.(zip|tar\.gz|tgz|tar\.zst|tzst|tar|7z)$/i
const PREVIEW = /\.(png|jpe?g|gif|webp|avif|pdf|mp4|webm|mov|mp3|ogg|wav|m4a)$/i

export default function FilesPage() {
  const t = useT()
  const nav = useNavigate()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const write = can(user, 'files.write')
  const canSftp = can(user, 'system.manage')
  const star = useParams()['*'] ?? ''
  const [roots, setRoots] = useState<string[]>([])
  const path = '/' + star.replace(/^\/+|\/+$/g, '')
  const [entries, setEntries] = useState<Entry[]>([])
  const [err, setErr] = useState('')
  const [hidden, setHidden] = useState(() => localStorage.getItem('berth.files.hidden') === '1')
  const [sort, setSort] = useState<'name' | 'size' | 'mtime'>('name')
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [edit, setEdit] = useState<Entry | null>(null)
  const [perm, setPerm] = useState<Entry | null>(null)
  const [share, setShare] = useState<string | null>(null)
  const [prompt, setPrompt] = useState<{ kind: 'file' | 'dir' | 'rename' | 'archive' | 'extract'; entry?: Entry } | null>(null)
  const [uploads, setUploads] = useState<Record<string, number>>({})
  const [search, setSearch] = useState<{ q: string; content: boolean; results?: string[] } | null>(null)
  const [favs, setFavs] = useState<string[]>(() => JSON.parse(localStorage.getItem('berth.files.favs') ?? '[]'))
  const fileInput = useRef<HTMLInputElement>(null)
  const dirInput = useRef<HTMLInputElement>(null)
  const go = (p: string) => { setSel(new Set()); nav(hostPath(host, 'files' + p)) }

  useEffect(() => { get<{ roots: string[] }>(`/api/v1/hosts/${host}/files/roots`).then((r) => setRoots(r.roots)).catch(() => {}) }, [host])
  useEffect(() => {
    if (roots.length && path === '/' && !roots.includes('/')) go(roots[0])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [roots, path])
  const load = async () => {
    setErr('')
    try {
      const r = await get<{ result: Entry[] }>(`/api/v1/hosts/${host}/files/list?path=${encodeURIComponent(path)}${hidden ? '&hidden=1' : ''}`)
      setEntries(r.result ?? [])
    } catch (e) { setEntries([]); setErr((e as ApiError).message) }
  }
  useEffect(() => { void load() }, [path, hidden, host]) // eslint-disable-line react-hooks/exhaustive-deps
  const sorted = useMemo(() => [...entries].sort((a, b) => (a.dir !== b.dir ? (a.dir ? -1 : 1) : sort === 'size' ? b.size - a.size : sort === 'mtime' ? b.mtime - a.mtime : a.name.localeCompare(b.name))), [entries, sort])
  const crumbs = path.split('/').filter(Boolean)
  const done = () => { setSel(new Set()); void load() }
  const selected = sorted.filter((e) => sel.has(e.path))

  const upload = async (files: FileList | null) => {
    if (!files?.length) return
    const list = [...files]
    const errs = await runQueue(list, (f) => uploadFile({
      host, file: f, dest: join(path, (f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name),
      onProgress: (s, total) => setUploads((u) => ({ ...u, [f.name]: s / total })),
    }))
    setUploads({})
    if (errs.length) setErr((errs[0] as Error).message)
    void load()
  }
  const toggleFav = () => {
    const next = favs.includes(path) ? favs.filter((f) => f !== path) : [...favs, path]
    setFavs(next)
    localStorage.setItem('berth.files.favs', JSON.stringify(next))
  }
  const download = (paths: string[]) => {
    const q = paths.map((p) => 'path=' + encodeURIComponent(p)).join('&')
    window.location.href = `/api/v1/hosts/${host}/files/download?${q}`
  }
  const rowMenu = (e: Entry) => [
    ...(!e.dir ? [{ label: t('files.edit'), onClick: () => setEdit(e) }, { label: t('files.download'), onClick: () => download([e.path]) }, { label: t('share.make'), onClick: () => setShare(e.path) }] : []),
    ...(write ? [
      { label: t('files.rename'), onClick: () => setPrompt({ kind: 'rename', entry: e }) },
      { label: t('files.permissions'), onClick: () => setPerm(e) },
      ...(ARCHIVE.test(e.name) ? [{ label: t('files.extract'), onClick: () => setPrompt({ kind: 'extract', entry: e }) }] : []),
      'sep' as const,
      { label: t('files.trash'), danger: true, onClick: () => run('file.trash', { paths: [e.path] }, { host, onDone: done }) },
    ] : []),
  ]
  const [dragging, setDragging] = useState(false)
  return (
    <div className={cx('space-y-3', dragging && 'rounded-[var(--radius-box)] outline-2 outline-dashed outline-acc')}
      onDragOver={(e) => { if (write && e.dataTransfer.types.includes('Files')) { e.preventDefault(); setDragging(true) } }}
      onDragLeave={(e) => { if (e.currentTarget === e.target) setDragging(false) }}
      onDrop={(e) => { e.preventDefault(); setDragging(false); if (write) void upload(e.dataTransfer.files) }}>
      {err && <Notice tone="bad">{err}</Notice>}
      {dragging && <Notice>{t('files.drop_here', { dir: path })}</Notice>}
      {(() => { const l = folderLabel(path); return l && (
        <div className={cx('rounded-[var(--radius-box)] border px-3.5 py-2 text-[12.5px]', l.danger ? 'border-warn/50 bg-warn/10' : 'border-line bg-soft')}>
          <b className="font-semibold">{t('flabel.' + l.key, { name: l.arg ?? '' })}</b>：{t('flabel.' + l.key + '_d', { name: l.arg ?? '' })}{l.danger ? ' ' + t('flabel.danger_d') : ''}
        </div>) })()}
      <div className="flex flex-wrap items-center gap-2">
        <nav className="flex min-w-0 flex-1 flex-wrap items-center gap-0.5 text-[13.5px]" aria-label={t('files.path')}>
          <button className="rounded px-1 hover:bg-soft" onClick={() => go('/')}>/</button>
          {crumbs.map((c, i) => (
            <span key={i} className="inline-flex items-center">
              {i > 0 && <ChevronRight className="size-3.5 text-mute" />}
              <button className={cx('rounded px-1 hover:bg-soft', i === crumbs.length - 1 && 'font-semibold')} onClick={() => go('/' + crumbs.slice(0, i + 1).join('/'))}>{c}</button>
            </span>
          ))}
          <button aria-label={t('files.favorite')} onClick={toggleFav} className="ml-1 text-mute hover:text-fg"><Star className={cx('size-4', favs.includes(path) && 'fill-[#f2c94c] text-[#f2c94c]')} /></button>
        </nav>
        <Button size="sm" onClick={() => setSearch({ q: '', content: false })}><Search className="size-3.5" /><span className="max-sm:hidden">{t('common.search')}</span></Button>
        {canSftp && <AvScanButton paths={sel.size ? [...sel] : [path]} />}
        {canSftp && <SftpButton dir={path} />}
        {canSftp && <SyncButton path={path} />}
        {write && <>
          <Button size="sm" onClick={() => setPrompt({ kind: 'dir' })}><FolderPlus className="size-3.5" /><span className="max-sm:hidden">{t('files.new_folder')}</span></Button>
          <Button size="sm" onClick={() => setPrompt({ kind: 'file' })}><FilePlus className="size-3.5" /><span className="max-sm:hidden">{t('files.new_file')}</span></Button>
          <Menu trigger={<Button size="sm" variant="primary"><UploadIcon className="size-3.5" />{t('files.upload')}</Button>} items={[
            { label: t('files.upload_files'), onClick: () => fileInput.current?.click() },
            { label: t('files.fetch_url'), onClick: () => { const u = window.prompt(t('files.fetch_prompt')); if (u) run('file.fetch', { url: u.trim(), path }, { host, onDone: done }) } },
            { label: t('files.upload_folder'), onClick: () => dirInput.current?.click() },
          ]} />
          <input ref={fileInput} type="file" multiple hidden onChange={(e) => void upload(e.target.files)} />
          <input ref={dirInput} type="file" hidden {...{ webkitdirectory: '' }} onChange={(e) => void upload(e.target.files)} />
        </>}
      </div>
      {favs.length > 0 && (
        <div className="flex flex-wrap gap-1.5 text-[12.5px]">{favs.map((f) => <button key={f} onClick={() => go(f)} className="num rounded-full border border-line px-2 hover:bg-soft">{f}</button>)}</div>
      )}
      {Object.keys(uploads).length > 0 && (
        <Box>{Object.entries(uploads).map(([n, p]) => (
          <div key={n} className="flex items-center gap-3 px-3.5 py-2 text-[12.5px]"><span className="min-w-0 flex-1 truncate">{n}</span>
            <span className="h-1.5 w-40 overflow-hidden rounded-full bg-soft"><span className="block h-full bg-acc" style={{ width: p * 100 + '%' }} /></span><span className="num w-10 text-right">{Math.round(p * 100)}%</span></div>
        ))}</Box>
      )}
      <Box
        title={sel.size ? t('files.selected', { n: sel.size }) : undefined}
        right={sel.size > 0 ? <>
          <Button size="sm" onClick={() => download(selected.map((e) => e.path))}><Download className="size-3.5" />{t('files.download')}</Button>
          {write && <Button size="sm" onClick={() => setPrompt({ kind: 'archive' })}><FileArchive className="size-3.5" />{t('files.compress')}</Button>}
          {write && <Button size="sm" variant="danger" onClick={() => run('file.trash', { paths: [...sel] }, { host, onDone: done, confirmName: undefined })}><Trash2 className="size-3.5" />{t('files.trash')}</Button>}
        </> : undefined}>
        <div className="flex items-center gap-3 border-b border-line bg-soft px-3.5 py-1.5 text-[12px] text-mute">
          <input type="checkbox" aria-label={t('files.select_all')} checked={sel.size > 0 && sel.size === sorted.length} onChange={(e) => setSel(e.target.checked ? new Set(sorted.map((x) => x.path)) : new Set())} />
          <button className="flex-1 text-left" onClick={() => setSort('name')}>{t('files.name')}{sort === 'name' && ' ↑'}</button>
          <button className="w-20 text-right" onClick={() => setSort('size')}>{t('files.size')}{sort === 'size' && ' ↓'}</button>
          <button className="hidden w-32 text-right md:block" onClick={() => setSort('mtime')}>{t('files.mtime')}{sort === 'mtime' && ' ↓'}</button>
          <span className="hidden w-36 lg:block">{t('files.perm')}</span>
          <label className="flex items-center gap-1"><input type="checkbox" checked={hidden} onChange={(e) => { setHidden(e.target.checked); localStorage.setItem('berth.files.hidden', e.target.checked ? '1' : '0') }} />{t('files.hidden')}</label>
          <span className="w-8" />
        </div>
        {path !== '/' && !roots.includes(path) && (
          <div className="flex cursor-pointer items-center gap-3 border-b border-line px-3.5 py-2 text-[13.5px] hover:bg-soft" onClick={() => go(parent(path))}>
            <span className="w-[13px]" /><Folder className="size-4 text-mute" />..</div>
        )}
        {sorted.length === 0 && !err && <Empty title={t('files.empty')} />}
        {sorted.map((e) => (
          <div key={e.path} className={cx('flex items-center gap-3 border-b border-line px-3.5 py-2 text-[13.5px] last:border-b-0 hover:bg-soft', sel.has(e.path) && 'bg-tint')}>
            <input type="checkbox" aria-label={e.name} checked={sel.has(e.path)} onChange={(x) => { const n = new Set(sel); if (x.target.checked) n.add(e.path); else n.delete(e.path); setSel(n) }} />
            <button className="flex min-w-0 flex-1 items-center gap-2 text-left" onClick={() => (e.dir ? go(e.path) : setEdit(e))}>
              {e.dir ? <Folder className="size-4 shrink-0 text-acc" /> : <File className="size-4 shrink-0 text-mute" />}
              <span className="truncate">{e.name}</span>
              {e.link && <span title={e.link_target}><Link2 className="size-3.5 text-mute" /></span>}
              {(() => { const l = e.dir ? folderLabel(e.path) : null; return l && (
                <span title={t('flabel.' + l.key + '_d', { name: l.arg ?? '' })} className={cx('shrink-0 truncate rounded px-1.5 text-[11.5px]', l.danger ? 'bg-warn/15 text-warn' : 'bg-tint text-acc')}>
                  {t('flabel.' + l.key, { name: l.arg ?? '' })}{l.danger ? ' · ' + t('flabel.danger') : ''}</span>) })()}
            </button>
            <span className="num w-20 text-right text-[12px] text-mute">{e.dir ? '' : bytes(e.size)}</span>
            <span className="num hidden w-32 text-right text-[12px] text-mute md:block">{datetime(e.mtime)}</span>
            <span className="num hidden w-36 truncate text-[12px] text-mute lg:block">{e.perm} {e.owner}:{e.group}</span>
            <span onClick={(x) => x.stopPropagation()}><Menu trigger={<Button size="sm" variant="ghost" aria-label={t('common.more')}><MoreHorizontal className="size-4" /></Button>} items={rowMenu(e)} /></span>
          </div>
        ))}
      </Box>
      <div className="text-right"><button className="text-[12.5px] text-acc hover:underline" onClick={() => nav(hostPath(host, 'files-trash'))}>{t('files.open_trash')}</button></div>
      {edit && <FileEditor entry={edit} onClose={() => setEdit(null)} onSaved={done} />}
      {perm && <PermDialog entry={perm} onClose={() => setPerm(null)} onDone={done} />}
      {share && <ShareDialog path={share} onClose={() => setShare(null)} />}
      {prompt && <NamePrompt kind={prompt.kind} entry={prompt.entry} dir={path} selected={selected} onClose={() => setPrompt(null)} onDone={done} />}
      {search && <SearchDialog dir={path} onClose={() => setSearch(null)} onOpen={(p) => { setSearch(null); go(parent(p)) }} />}
    </div>
  )
}

function NamePrompt({ kind, entry, dir, selected, onClose, onDone }: { kind: 'file' | 'dir' | 'rename' | 'archive' | 'extract'; entry?: Entry; dir: string; selected: Entry[]; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const initial = kind === 'rename' ? entry!.name : kind === 'archive' ? (selected[0]?.name ?? 'archive') + '.tar.zst' : kind === 'extract' ? entry!.name.replace(ARCHIVE, '') : ''
  const [name, setName] = useState(initial)
  const submit = () => {
    onClose()
    const target = join(dir, name)
    if (kind === 'dir') run('file.mkdir', { path: target }, { host, onDone })
    if (kind === 'file') run('file.write', { path: target, content: '' }, { host, onDone })
    if (kind === 'rename') run('file.rename', { path: entry!.path, to: target }, { host, onDone })
    if (kind === 'archive') run('file.archive', { paths: selected.map((e) => e.path), dest: target }, { host, onDone })
    if (kind === 'extract') run('file.extract', { path: entry!.path, dest: target }, { host, onDone })
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('files.prompt_' + kind)}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button><Button variant="primary" disabled={!name || name.includes('/')} onClick={submit}>{t('common.next')}</Button></>}>
      <form onSubmit={(e) => { e.preventDefault(); if (name) submit() }} className="px-4 py-3">
        <Input autoFocus value={name} onChange={(e) => setName(e.target.value)} aria-label={t('files.name')} />
        {kind === 'archive' && <p className="mt-1.5 text-[12px] text-mute">{t('files.archive_formats')}</p>}
      </form>
    </Dialog>
  )
}

function PermDialog({ entry, onClose, onDone }: { entry: Entry; onClose: () => void; onDone: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const [mode, setMode] = useState(entry.mode.replace(/^0(?=\d{3}$)/, ''))
  const [owner, setOwner] = useState(entry.owner)
  const [group, setGroup] = useState(entry.group)
  const [recursive, setRecursive] = useState(false)
  const n = parseInt(mode.padStart(3, '0').slice(-3), 8) || 0
  const bit = (who: number, b: number) => !!(n & (b << (who * 3)))
  const flip = (who: number, b: number) => setMode((n ^ (b << (who * 3))).toString(8).padStart(3, '0'))
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('files.permissions')} description={<span className="num">{entry.path}</span>}
      footer={<><Button className="ml-auto" onClick={onClose}>{t('common.cancel')}</Button>
        <Button variant="primary" disabled={!/^[0-7]{3,4}$/.test(mode)} onClick={() => { onClose(); run('file.chmod', { path: entry.path, mode, owner: owner !== entry.owner ? owner : '', group: group !== entry.group ? group : '', recursive }, { host, onDone }) }}>{t('common.next')}</Button></>}>
      <table className="mx-4 mt-3 text-[13px]">
        <thead><tr className="text-mute"><th /><th className="px-3 font-normal">{t('perm.read')}</th><th className="px-3 font-normal">{t('perm.write')}</th><th className="px-3 font-normal">{t('perm.exec')}</th></tr></thead>
        <tbody>{[[2, 'perm.owner'], [1, 'perm.group'], [0, 'perm.others']].map(([who, label]) => (
          <tr key={label}><td className="pr-3 text-mute">{t(label as string)}</td>{[4, 2, 1].map((b) => <td key={b} className="px-3 text-center"><input type="checkbox" checked={bit(who as number, b)} onChange={() => flip(who as number, b)} aria-label={`${t(label as string)} ${b}`} /></td>)}</tr>
        ))}</tbody>
      </table>
      <Field label={t('perm.numeric')}><Input className="num w-24" value={mode} onChange={(e) => setMode(e.target.value.replace(/[^0-7]/g, '').slice(0, 4))} /></Field>
      <div className="grid grid-cols-2"><Field label={t('perm.owner')}><Input value={owner} onChange={(e) => setOwner(e.target.value)} /></Field><Field label={t('perm.group')}><Input value={group} onChange={(e) => setGroup(e.target.value)} /></Field></div>
      {entry.dir && <div className="px-4 py-3"><Switch checked={recursive} onChange={setRecursive} label={t('perm.recursive')} /></div>}
      <div className="h-3" />
    </Dialog>
  )
}

function FileEditor({ entry, onClose, onSaved }: { entry: Entry; onClose: () => void; onSaved: () => void }) {
  const t = useT()
  const run = useChange()
  const { host } = useHost()
  const { user } = useAuth()
  const [state, setState] = useState<{ text: string; mtime: number; truncated: boolean; binary: boolean; size: number } | null>(null)
  const [draft, setDraft] = useState('')
  const [err, setErr] = useState('')
  const preview = PREVIEW.test(entry.name)
  const src = `/api/v1/hosts/${host}/files/download?inline=1&path=${encodeURIComponent(entry.path)}`
  useEffect(() => {
    if (preview) return
    get<{ result: { content: string; mtime: number; truncated: boolean; binary: boolean; size: number } }>(`/api/v1/hosts/${host}/files/read?path=${encodeURIComponent(entry.path)}&max=2097152`)
      .then(({ result: r }) => {
        const text = r.binary ? '' : new TextDecoder().decode(Uint8Array.from(atob(r.content ?? ''), (c) => c.charCodeAt(0)))
        setState({ text, mtime: r.mtime, truncated: r.truncated, binary: r.binary, size: r.size })
        setDraft(text)
      }).catch((e: ApiError) => setErr(e.message))
  }, [entry.path, host, preview])
  const readOnly = !can(user, 'files.write') || !!state?.truncated || !!state?.binary
  const save = () => run('file.write', { path: entry.path, content: draft, expect_mtime: state!.mtime }, { host, onDone: () => { onSaved(); onClose() } })
  return (
    <Drawer open onOpenChange={(o) => !o && onClose()} title={<span className="num">{entry.path}</span>}
      right={<a href={`/api/v1/hosts/${host}/files/download?path=${encodeURIComponent(entry.path)}`}><Button size="sm"><Download className="size-3.5" />{t('files.download')}</Button></a>}
      footer={!preview && state && !readOnly && <>
        <span className="text-[12.5px] text-mute">{draft !== state.text ? t('compose.unsaved') : bytes(state.size)}</span>
        <Button className="ml-auto" variant="primary" disabled={draft === state.text} onClick={save}>{t('action.file.write')}</Button>
      </>}>
      <div className="h-full p-3">
        {err && <Notice tone="bad">{err}</Notice>}
        {preview && (/\.pdf$/i.test(entry.name) ? <iframe title={entry.name} src={src} className="h-[80vh] w-full rounded border border-line" />
          : /\.(mp4|webm|mov)$/i.test(entry.name) ? <video src={src} controls className="max-h-[80vh] max-w-full" />
            : /\.(mp3|ogg|wav|m4a)$/i.test(entry.name) ? <audio src={src} controls />
              : <img src={src} alt={entry.name} className="max-h-[80vh] max-w-full rounded border border-line" />)}
        {state?.binary && <Empty title={t('files.binary')}>{t('files.binary_hint')}</Empty>}
        {state && !state.binary && <>
          {state.truncated && <div className="mb-2"><Pill tone="warn">{t('files.too_large')}</Pill></div>}
          <CodeEditor value={draft} onChange={setDraft} language={languageOf(entry.name)} readOnly={readOnly} height="calc(100vh - 170px)" />
        </>}
      </div>
    </Drawer>
  )
}

function SearchDialog({ dir, onClose, onOpen }: { dir: string; onClose: () => void; onOpen: (p: string) => void }) {
  const t = useT()
  const { host } = useHost()
  const [q, setQ] = useState('')
  const [content, setContent] = useState(false)
  const [res, setRes] = useState<string[] | null>(null)
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      const r = await get<{ result: string[] }>(`/api/v1/hosts/${host}/files/search?path=${encodeURIComponent(dir)}&${content ? 'q' : 'name'}=${encodeURIComponent(q)}`)
      setRes(r.result ?? [])
    } finally { setBusy(false) }
  }
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={t('files.search_in', { dir })} wide>
      <form onSubmit={(e) => { e.preventDefault(); void go() }} className="flex items-center gap-2 px-4 py-3">
        <Input autoFocus value={q} onChange={(e) => setQ(e.target.value)} aria-label={t('common.search')} />
        <Switch checked={content} onChange={setContent} label={t('files.search_content')} />
        <Button type="submit" variant="primary" disabled={!q || busy}>{t('common.search')}</Button>
      </form>
      {res && (res.length === 0 ? <Empty title={t('files.no_results')} /> : (
        <ul className="max-h-[50vh] overflow-auto border-t border-line">{res.map((p) => <li key={p}><button onClick={() => onOpen(p)} className="num w-full truncate px-4 py-1.5 text-left text-[12.5px] hover:bg-soft">{p}</button></li>)}</ul>
      ))}
    </Dialog>
  )
}
