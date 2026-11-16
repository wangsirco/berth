#!/usr/bin/env node
// Copy the backend's change titles and button labels (kind.*, action.*) into the UI
// dictionaries, so menus and task lists name changes exactly as the server does.
import { readFileSync, writeFileSync } from 'node:fs'
const locs = ['zh-CN', 'zh-TW', 'en']
let n = 0
for (const l of locs) {
  const be = JSON.parse(readFileSync(new URL(`../../internal/panel/i18n/${l}.json`, import.meta.url), 'utf8'))
  const fePath = new URL(`../src/i18n/${l}.json`, import.meta.url)
  const fe = JSON.parse(readFileSync(fePath, 'utf8'))
  for (const [k, v] of Object.entries(be)) {
    if ((k.startsWith('kind.') || k.startsWith('action.')) && fe[k] !== v) { fe[k] = v; n++ }
  }
  const sorted = Object.fromEntries(Object.entries(fe).sort(([a], [b]) => a.localeCompare(b)))
  writeFileSync(fePath, JSON.stringify(sorted, null, 1) + '\n')
}
if (n) console.log(`synced ${n} strings from the backend`)
