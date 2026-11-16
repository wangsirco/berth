// Each host gets a flag drawn from the geometry of the international maritime signal flags
// (方案 §10.2): the name decides the pattern and colours, so it never changes.

export type Pattern = 'solid' | 'h2' | 'v2' | 'h3' | 'v3' | 'quarter' | 'cross' | 'diag' | 'border' | 'square' | 'saltire' | 'checker'
export interface FlagSpec { pattern: Pattern; colors: string[] }

const PATTERNS: Pattern[] = ['solid', 'h2', 'v2', 'h3', 'v3', 'quarter', 'cross', 'diag', 'border', 'square', 'saltire', 'checker']
const COLORS_N: Record<Pattern, number> = { solid: 1, h2: 2, v2: 2, h3: 3, v3: 3, quarter: 2, cross: 2, diag: 2, border: 2, square: 2, saltire: 2, checker: 2 }

const LIGHT = ['#cf222e', '#1f4e8c', '#f2c94c', '#ffffff', '#1f2328']
const DARK = ['#cf222e', '#4f7fc4', '#f2c94c', '#d1d9e0', '#545d68']

function fnv1a(s: string): number {
  let h = 0x811c9dc5
  for (const ch of new TextEncoder().encode(s)) {
    h ^= ch
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h >>> 0
}

export function signalFlag(name: string, dark: boolean): FlagSpec {
  let h = fnv1a(name.toLowerCase())
  const pattern = PATTERNS[(h & 0xf) % PATTERNS.length]
  h = Math.imul(h ^ (h >>> 13), 0x5bd1e995) >>> 0
  const n = COLORS_N[pattern]
  const idx: number[] = []
  let x = h
  while (idx.length < n) {
    let c = x % 5
    x = Math.floor(x / 5) || 0x9e3779b9 + idx.length
    // neighbouring stripes differ; three-colour flags use three different colours
    while (idx.includes(c) && (idx[idx.length - 1] === c || n === 3)) c = (c + 1) % 5
    idx.push(c)
  }
  const pal = dark ? DARK : LIGHT
  return { pattern, colors: idx.map((i) => pal[i]) }
}
