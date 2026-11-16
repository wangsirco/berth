// @vitest-environment node
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const css = readFileSync(fileURLToPath(new URL('../src/index.css', import.meta.url)), 'utf8')
function vars(selector: string) {
  const block = css.split(selector + ' {')[1].split('}')[0]
  return Object.fromEntries([...block.matchAll(/--([\w-]+):\s*(#[0-9a-f]{6})/gi)].map(m => [m[1], m[2]]))
}
function lum(hex: string) {
  const c = [1, 3, 5].map(i => parseInt(hex.slice(i, i + 2), 16) / 255).map(v => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4))
  return 0.2126 * c[0] + 0.7152 * c[1] + 0.0722 * c[2]
}
const ratio = (a: string, b: string) => { const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p); return (x + 0.05) / (y + 0.05) }
const pairs: [string, string][] = [['fg', 'canvas'], ['mute', 'canvas'], ['fg', 'soft'], ['mute', 'soft'], ['acc', 'canvas'], ['acc-fg', 'acc'], ['ok', 'canvas'], ['warn', 'canvas'], ['bad', 'canvas']]
for (const sel of [':root', '.dark']) {
  describe(sel, () => {
    const v = vars(sel)
    for (const [a, b] of pairs) it(`${a} on ${b} ≥ 4.5`, () => expect(ratio(v[a], v[b])).toBeGreaterThanOrEqual(4.5))
  })
}
