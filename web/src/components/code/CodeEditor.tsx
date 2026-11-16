import { lazy, Suspense } from 'react'
import { useT } from '@/lib/i18n'

export interface CodeEditorProps {
  value: string
  onChange?: (v: string) => void
  language?: string // yaml | json | js | ts | py | md | html | css | text
  readOnly?: boolean
  height?: string
}

// CodeMirror is loaded only when an editor is shown (keeps the first page small).
const Impl = lazy(() => import('./CodeMirrorImpl'))

export function CodeEditor(p: CodeEditorProps) {
  const t = useT()
  return (
    <Suspense fallback={<div className="p-3 text-[12.5px] text-mute">{t('common.loading')}</div>}>
      <Impl {...p} />
    </Suspense>
  )
}

export function languageOf(path: string): string {
  const ext = path.split('.').pop()?.toLowerCase() ?? ''
  if (['yml', 'yaml'].includes(ext)) return 'yaml'
  if (ext === 'json') return 'json'
  if (['js', 'mjs', 'cjs', 'jsx'].includes(ext)) return 'js'
  if (['ts', 'tsx'].includes(ext)) return 'ts'
  if (ext === 'py') return 'py'
  if (['md', 'markdown'].includes(ext)) return 'md'
  if (['html', 'htm'].includes(ext)) return 'html'
  if (['css', 'scss'].includes(ext)) return 'css'
  return 'text'
}
