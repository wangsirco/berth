import { Switch as S } from '@base-ui/react/switch'
import { cloneElement, forwardRef, isValidElement, useId, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import { cx } from './cx'

const ctlBase = 'rounded-[var(--radius-box)] border border-line bg-canvas px-2.5 text-[14px] outline-none focus:border-acc focus:ring-1 focus:ring-acc disabled:opacity-60'
// full width unless the caller gives a width (two w-* classes would fight by CSS order)
const ctlFor = (className?: string) => (/(^|\s)w-/.test(className ?? '') ? ctlBase : 'w-full ' + ctlBase)

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input({ className, ...p }, ref) {
  return <input ref={ref} className={cx(ctlFor(className), 'h-8', className)} {...p} />
})

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaHTMLAttributes<HTMLTextAreaElement>>(function Textarea({ className, ...p }, ref) {
  return <textarea ref={ref} className={cx(ctlFor(className), 'py-1.5', className)} {...p} />
})

export function Select({ className, children, ...p }: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select className={cx(ctlFor(className), 'h-8 pr-7', className)} {...p}>{children}</select>
}

// Label names a control; the hint stays outside the <label> so the accessible name is just
// the label text (the hint is linked as a description by Field).
export function Label({ children, hint, htmlFor, hintId }: { children: ReactNode; hint?: ReactNode; htmlFor?: string; hintId?: string }) {
  return (
    <div className="mb-1.5 text-[12.5px]">
      <label htmlFor={htmlFor} className="font-semibold">{children}</label>
      {hint && <span id={hintId} className="ml-1.5 text-mute">{hint}</span>}
    </div>
  )
}

// Field labels its control: a single input/select/textarea child gets the label's id.
export function Field({ label, hint, children, className }: { label: ReactNode; hint?: ReactNode; children: ReactNode; className?: string }) {
  const id = useId()
  const single = isValidElement<{ id?: string; 'aria-describedby'?: string }>(children) ? children : null
  const target = single?.props.id ?? id
  const hintId = hint ? id + '-hint' : undefined
  return (
    <div className={cx('px-4 pt-3', className)}>
      <Label hint={hint} htmlFor={single ? target : undefined} hintId={hintId}>{label}</Label>
      {single ? cloneElement(single, { id: target, 'aria-describedby': hintId }) : children}
    </div>
  )
}

export function Switch({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label: ReactNode; disabled?: boolean }) {
  return (
    <label className="inline-flex cursor-pointer items-center gap-2 text-[13px]">
      <S.Root checked={checked} onCheckedChange={onChange} disabled={disabled}
        className="relative h-5 w-9 rounded-full border border-line bg-soft transition-colors data-[checked]:border-acc data-[checked]:bg-acc data-[disabled]:opacity-50">
        <S.Thumb className="block size-4 translate-x-0.5 rounded-full bg-canvas shadow-sm transition-transform data-[checked]:translate-x-[17px]" />
      </S.Root>
      {label}
    </label>
  )
}

// Segmented: the design's dark-selected segment control.
export function Segmented<T extends string>({ value, options, onChange }: { value: T; options: { value: T; label: ReactNode }[]; onChange: (v: T) => void }) {
  return (
    <div role="radiogroup" className="inline-flex overflow-hidden rounded-[var(--radius-box)] border border-line text-[12.5px]">
      {options.map((o) => (
        <button key={o.value} role="radio" aria-checked={value === o.value} onClick={() => onChange(o.value)} type="button"
          className={cx('border-r border-line px-2.5 py-1 last:border-r-0', value === o.value ? 'bg-fg text-canvas' : 'hover:bg-soft')}>
          {o.label}
        </button>
      ))}
    </div>
  )
}
