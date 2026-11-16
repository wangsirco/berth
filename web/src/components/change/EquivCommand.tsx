import { Check, Copy } from 'lucide-react'
import { useState } from 'react'
import { useT } from '@/lib/i18n'

// The dark "equivalent command" box at the bottom of every confirmation (方案 §10.2).
export function EquivCommand({ command }: { command: string }) {
  const t = useT()
  const [copied, setCopied] = useState(false)
  if (!command) return null
  const copy = async () => {
    try { await navigator.clipboard.writeText(command) } catch { /* clipboard may be blocked */ }
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }
  const [head, ...rest] = command.split(' ')
  return (
    <div className="flex items-start gap-2 rounded-[var(--radius-box)] bg-term px-3 py-2 text-term-fg">
      <div className="num min-w-0 flex-1 text-[12px] leading-relaxed">
        <div className="text-[#8c959f]"># {t('change.equivalent_hint')}</div>
        <span data-testid="equiv" className="break-all"><span className="text-[#7ee2d6]">{head}</span> {rest.join(' ')}</span>
      </div>
      <button onClick={copy} className="mt-0.5 inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11.5px] text-[#8c959f] hover:bg-white/10 hover:text-white" aria-live="polite">
        {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
        {copied ? t('common.copied') : t('common.copy')}
      </button>
    </div>
  )
}
