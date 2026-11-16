import { X } from 'lucide-react'
import { useRef, type ClipboardEvent, type KeyboardEvent } from 'react'
import { cx } from './cx'

// splitTags: what a typed or pasted text holds, one item per space, comma or line.
export const splitTags = (s: string) => s.split(/[\s,，;；]+/).map((x) => x.trim()).filter(Boolean)

// TagInput: one tag per item (domains, addresses). Enter, space or comma turns what is typed into a
// tag; Backspace in an empty box takes the last one back; a pasted list is split. The text not yet
// turned into a tag is the parent's (draft), so a form sent while typing still counts it.
// With primary, the first tag is marked and clicking another moves it to the front.
export function TagInput({ value, onChange, draft, onDraft, placeholder, primary, primaryLabel, makePrimaryLabel, autoFocus, invalid, ariaLabel }: {
  value: string[]; onChange: (v: string[]) => void; draft: string; onDraft: (s: string) => void
  placeholder?: string; primary?: boolean; primaryLabel?: string; makePrimaryLabel?: string; autoFocus?: boolean
  invalid?: (tag: string) => boolean; ariaLabel?: string
}) {
  const ref = useRef<HTMLInputElement>(null)
  const add = (items: string[]) => {
    const next = [...value]
    for (const x of items) if (!next.includes(x)) next.push(x)
    onChange(next)
  }
  const commit = () => {
    const items = splitTags(draft)
    if (items.length) add(items)
    onDraft('')
  }
  const key = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return
    if (e.key === 'Enter' || e.key === ' ' || e.key === ',' || e.key === '，' || (e.key === 'Tab' && draft.trim())) {
      if (!draft.trim()) { if (e.key !== 'Enter') e.preventDefault(); return }
      e.preventDefault()
      commit()
    } else if (e.key === 'Backspace' && draft === '' && value.length) {
      e.preventDefault()
      onDraft(value[value.length - 1])
      onChange(value.slice(0, -1))
    }
  }
  const paste = (e: ClipboardEvent<HTMLInputElement>) => {
    const items = splitTags(e.clipboardData.getData('text'))
    if (items.length > 1) { e.preventDefault(); add(items); onDraft('') }
  }
  return (
    <div onClick={() => ref.current?.focus()}
      className="flex min-h-8 w-full cursor-text flex-wrap items-center gap-1.5 rounded-[var(--radius-box)] border border-line bg-canvas px-1.5 py-1 focus-within:border-acc focus-within:ring-1 focus-within:ring-acc">
      {value.map((tag, i) => (
        <span key={tag} className={cx('inline-flex max-w-full items-center gap-1 rounded-md border py-0.5 pl-2 pr-1 text-[13px]',
          invalid?.(tag) ? 'border-bad text-bad' : i === 0 && primary ? 'border-acc bg-tint' : 'border-line bg-[color-mix(in_srgb,var(--line)_35%,transparent)]')}>
          <button type="button" className={cx('num truncate', primary && i > 0 && 'hover:text-acc')} disabled={!primary || i === 0}
            title={primary && i > 0 ? makePrimaryLabel : undefined}
            onClick={(e) => { e.stopPropagation(); onChange([tag, ...value.filter((x) => x !== tag)]) }}>{tag}</button>
          {primary && i === 0 && value.length > 1 && <span className="text-[11px] font-semibold text-acc">{primaryLabel}</span>}
          <button type="button" aria-label={'× ' + tag} className="rounded p-0.5 text-mute hover:bg-[color-mix(in_srgb,var(--line)_60%,transparent)] hover:text-fg"
            onClick={(e) => { e.stopPropagation(); onChange(value.filter((x) => x !== tag)) }}><X className="size-3" /></button>
        </span>
      ))}
      <input ref={ref} value={draft} onChange={(e) => onDraft(e.target.value)} onKeyDown={key} onPaste={paste} onBlur={commit}
        autoFocus={autoFocus} aria-label={ariaLabel} placeholder={value.length ? '' : placeholder}
        className="min-w-[8rem] flex-1 bg-transparent py-0.5 text-[13.5px] outline-none placeholder:text-mute" />
    </div>
  )
}
