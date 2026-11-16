import { useTheme } from '@/lib/theme'
import { signalFlag, type FlagSpec } from './signalFlag'

function shapes({ pattern, colors: [a, b = a, c = a] }: FlagSpec) {
  switch (pattern) {
    case 'solid': return <rect width="12" height="12" fill={a} />
    case 'h2': return <><rect width="12" height="6" fill={a} /><rect y="6" width="12" height="6" fill={b} /></>
    case 'v2': return <><rect width="6" height="12" fill={a} /><rect x="6" width="6" height="12" fill={b} /></>
    case 'h3': return <><rect width="12" height="4" fill={a} /><rect y="4" width="12" height="4" fill={b} /><rect y="8" width="12" height="4" fill={c} /></>
    case 'v3': return <><rect width="4" height="12" fill={a} /><rect x="4" width="4" height="12" fill={b} /><rect x="8" width="4" height="12" fill={c} /></>
    case 'quarter': return <><rect width="12" height="12" fill={b} /><rect width="6" height="6" fill={a} /><rect x="6" y="6" width="6" height="6" fill={a} /></>
    case 'cross': return <><rect width="12" height="12" fill={a} /><rect x="4.5" width="3" height="12" fill={b} /><rect y="4.5" width="12" height="3" fill={b} /></>
    case 'diag': return <><rect width="12" height="12" fill={a} /><path d="M12 0V12H0Z" fill={b} /></>
    case 'border': return <><rect width="12" height="12" fill={a} /><rect x="3" y="3" width="6" height="6" fill={b} /></>
    case 'square': return <><rect width="12" height="12" fill={b} /><rect x="2" y="2" width="8" height="8" fill={a} /></>
    case 'saltire': return <><rect width="12" height="12" fill={a} /><path d="M0 0L12 12M12 0L0 12" stroke={b} strokeWidth="2.5" /></>
    case 'checker': return <>{[0, 1, 2, 3].flatMap((i) => [0, 1, 2, 3].map((j) => <rect key={`${i}${j}`} x={i * 3} y={j * 3} width="3" height="3" fill={(i + j) % 2 ? b : a} />))}</>
  }
}

export function SignalFlag({ name, size = 16 }: { name: string; size?: number }) {
  const { dark } = useTheme()
  const spec = signalFlag(name, dark)
  return (
    <svg width={size} height={size} viewBox="0 0 12 12" aria-hidden className="shrink-0" data-pattern={spec.pattern}>
      {shapes(spec)}
      <rect x=".25" y=".25" width="11.5" height="11.5" fill="none" stroke="rgba(0,0,0,.15)" strokeWidth=".5" />
    </svg>
  )
}
