import { forwardRef, type ButtonHTMLAttributes } from 'react'
import { cx } from './cx'

type Variant = 'default' | 'primary' | 'danger' | 'ghost'
const variants: Record<Variant, string> = {
  default: 'border-line bg-canvas hover:bg-soft',
  primary: 'border-black/10 bg-acc text-acc-fg hover:bg-acc-2',
  danger: 'border-line bg-canvas text-bad hover:border-bad hover:bg-bad hover:text-white',
  ghost: 'border-transparent bg-transparent hover:bg-soft',
}

export const Button = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; size?: 'sm' | 'md' }>(
  function Button({ variant = 'default', size = 'md', className, type = 'button', ...p }, ref) {
    return (
      <button
        ref={ref}
        type={type}
        className={cx(
          'inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-[var(--radius-box)] border font-medium transition-colors',
          'focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-acc disabled:cursor-not-allowed disabled:opacity-50',
          size === 'sm' ? 'h-7 px-2.5 text-[12.5px]' : 'h-8 px-3 text-[13px]',
          variants[variant],
          className,
        )}
        {...p}
      />
    )
  },
)
