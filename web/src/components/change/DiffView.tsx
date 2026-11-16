import { cx } from '../ui/cx'

type Line = { op: ' ' | '+' | '-'; text: string; a?: number; b?: number }
type Hunk = { header: string; lines: Line[] }

// parseUnified turns a unified diff into hunks with old/new line numbers.
export function parseUnified(diff: string): { path: string; hunks: Hunk[] } {
  const hunks: Hunk[] = []
  let path = ''
  let a = 0, b = 0
  for (const raw of diff.split('\n')) {
    if (raw.startsWith('+++ ')) { path = raw.slice(4).replace(/^b\//, '/').replace(/^\/\//, '/'); continue }
    if (raw.startsWith('--- ')) continue
    const m = raw.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/)
    if (raw.startsWith('@@')) {
      if (m) { a = +m[1]; b = +m[2] }
      hunks.push({ header: raw, lines: [] })
      continue
    }
    if (!hunks.length || raw === '') continue
    const op = raw[0] as Line['op']
    if (op !== ' ' && op !== '+' && op !== '-') continue
    const l: Line = { op, text: raw.slice(1) }
    if (op !== '+') l.a = a++
    if (op !== '-') l.b = b++
    hunks[hunks.length - 1].lines.push(l)
  }
  return { path, hunks }
}

export function DiffView({ diff, path }: { diff: string; path?: string }) {
  const p = parseUnified(diff)
  return (
    <div className="overflow-hidden rounded-[var(--radius-box)] border border-line">
      <div className="num border-b border-line bg-soft px-3 py-1.5 text-[12px]">{path ?? p.path}</div>
      <div className="overflow-x-auto">
        <table className="num w-full border-collapse text-[12px] leading-[20px]">
          <tbody>
            {p.hunks.map((h, i) => [
              <tr key={'h' + i} data-hunk><td colSpan={3} className="bg-tint px-3 text-mute">{h.header}</td></tr>,
              ...h.lines.map((l, j) => (
                <tr key={i + '-' + j} data-op={l.op}
                  className={cx(l.op === '-' && 'bg-[#ffebe9] dark:bg-[#3c1618]', l.op === '+' && 'bg-[#dafbe1] dark:bg-[#12261e]')}>
                  <td className="w-10 select-none border-r border-line px-2 text-right text-mute">{l.a ?? ''}</td>
                  <td className="w-10 select-none border-r border-line px-2 text-right text-mute">{l.b ?? ''}</td>
                  <td className="whitespace-pre px-2"><span className="select-none text-mute">{l.op}</span>{l.text}</td>
                </tr>
              )),
            ])}
          </tbody>
        </table>
      </div>
    </div>
  )
}
