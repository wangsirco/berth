#!/usr/bin/env node
// Merge UI strings into src/i18n/{zh-CN,zh-TW,en}.json.
// Usage: node scripts/i18n-add.mjs file.tsv   (key<TAB>zh-CN<TAB>zh-TW<TAB>en)
import { readFileSync, writeFileSync, existsSync } from 'node:fs'
const locs = ['zh-CN', 'zh-TW', 'en']
const dir = new URL('../src/i18n/', import.meta.url)
const data = Object.fromEntries(locs.map(l => {
  const f = new URL(l + '.json', dir)
  return [l, existsSync(f) ? JSON.parse(readFileSync(f, 'utf8')) : {}]
}))
for (const line of readFileSync(process.argv[2], 'utf8').split('\n')) {
  if (!line.trim() || line.startsWith('#')) continue
  const parts = line.split('\t')
  if (parts.length !== 4) { console.error('bad line:', line); process.exit(1) }
  locs.forEach((l, i) => { data[l][parts[0]] = parts[i + 1] })
}
for (const l of locs) {
  const sorted = Object.fromEntries(Object.entries(data[l]).sort(([a], [b]) => a.localeCompare(b)))
  writeFileSync(new URL(l + '.json', dir), JSON.stringify(sorted, null, 1) + '\n')
}
console.log(Object.fromEntries(locs.map(l => [l, Object.keys(data[l]).length])))
