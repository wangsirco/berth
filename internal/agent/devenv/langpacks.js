// Writes code-server's languagepacks.json from the installed language-pack extensions.
// The server only builds this index when a pack is installed from its own UI; packs
// installed from the command line (as boot.sh does, before the first start) are otherwise
// ignored and the editor stays in English.   usage: node langpacks.js <user-data-dir>
const fs = require('fs')
const path = require('path')
const crypto = require('crypto')
const data = process.argv[2]
const dir = path.join(data, 'extensions')
const out = {}
for (const e of fs.existsSync(dir) ? fs.readdirSync(dir) : []) {
  let m
  try { m = JSON.parse(fs.readFileSync(path.join(dir, e, 'package.json'), 'utf8')) } catch { continue }
  for (const l of (m.contributes && m.contributes.localizations) || []) {
    const id = (m.publisher + '.' + m.name).toLowerCase()
    const p = out[l.languageId] || (out[l.languageId] = { hash: '', extensions: [], translations: {}, label: l.localizedLanguageName || l.languageName })
    p.extensions.push({ extensionIdentifier: { id }, version: m.version })
    for (const t of l.translations || []) p.translations[t.id] = path.join(dir, e, t.path)
  }
}
for (const k of Object.keys(out)) {
  const h = crypto.createHash('md5')
  for (const x of out[k].extensions) h.update(x.extensionIdentifier.id).update(x.version)
  out[k].hash = h.digest('hex')
}
fs.writeFileSync(path.join(data, 'languagepacks.json'), JSON.stringify(out))
