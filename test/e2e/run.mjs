// End-to-end tests against a fresh throwaway panel on the dev server.
//   cd test/e2e && npm test            (BERTH_DEV_HOST=root@host to override)
// Steps: build the panel (with the UI embedded), start berth-panel-e2e on port 7092 with an
// empty database, tunnel it to localhost, and drive it with a local Chromium browser.
import { createHmac, randomBytes } from 'node:crypto'
import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync, unlinkSync, writeFileSync } from 'node:fs'
import net from 'node:net'
import { setTimeout as sleep } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const ROOT = fileURLToPath(new URL('../..', import.meta.url))
const HOST = process.env.BERTH_DEV_HOST ?? 'root@192.0.2.10'
const BASE = 'https://localhost:7092'
const OUT = fileURLToPath(new URL('./out/', import.meta.url))
mkdirSync(OUT, { recursive: true })
const CONTAINER = process.env.E2E_CONTAINER ?? 'smoke-web-1' // a disposable container on the dev server
const PROJECT = process.env.E2E_PROJECT ?? 'smoke'

const browserPath = [process.env.CHROME_PATH, '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge', '/Applications/Brave Browser.app/Contents/MacOS/Brave Browser',
  '/usr/bin/chromium', '/usr/bin/google-chrome'].find((p) => p && existsSync(p))
if (!browserPath) throw new Error('no Chromium-based browser found; set CHROME_PATH')

// ---- environment ----
const sh = (cmd, args, opts = {}) => execFileSync(cmd, args, { cwd: ROOT, stdio: ['ignore', 'pipe', 'inherit'], timeout: 600000, killSignal: 'SIGKILL', ...opts }).toString().trim()
if (!process.env.E2E_SKIP_BUILD) {
  console.log('building…')
  sh('npm', ['run', 'build', '--silent'], { cwd: ROOT + 'web' })
  sh('go', ['build', '-trimpath', '-o', 'bin/linux-arm64/berth-panel', './cmd/berth-panel'], { env: { ...process.env, PATH: '/opt/homebrew/bin:' + process.env.PATH, GOOS: 'linux', GOARCH: 'arm64', CGO_ENABLED: '0' } })
}
const setupCode = sh('sh', ['test/e2e/e2e-instance.sh'])
// a disposable, unmanaged compose project (smoke-web-1) for the container specs; removed at the end
let madeSmoke = false
if (!process.env.E2E_CONTAINER && sh('ssh', [HOST, `docker inspect ${CONTAINER} >/dev/null 2>&1 && echo y || echo n`]) === 'n') {
  sh('ssh', [HOST, `mkdir -p /srv/e2e-smoke && printf 'services:\\n  web:\\n    image: redis:7-alpine\\n' > /srv/e2e-smoke/compose.yaml && cd /srv/e2e-smoke && docker compose -p ${PROJECT} up -d >/dev/null 2>&1 && echo up`])
  madeSmoke = true
}
const tunnel = spawn('ssh', ['-N', '-o', 'ExitOnForwardFailure=yes', '-L', '7092:127.0.0.1:7092', HOST], { stdio: 'ignore' })
await sleep(1500)

// ---- helpers ----
const b32 = (s) => {
  const abc = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = ''
  for (const c of s.replace(/=+$/, '')) bits += abc.indexOf(c).toString(2).padStart(5, '0')
  return Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)))
}
function totp(secret, at = Date.now()) {
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)))
  const h = createHmac('sha1', b32(secret)).update(counter).digest()
  const o = h[h.length - 1] & 0xf
  return String((h.readUInt32BE(o) & 0x7fffffff) % 1e6).padStart(6, '0')
}
const results = []
const ONLY = process.env.E2E_ONLY?.split(',') // E2E_ONLY=vms,remote-desktop runs just those (after setup-login)
async function spec(name, fn) {
  if (ONLY && name !== 'setup-login' && !ONLY.includes(name)) return
  const t0 = Date.now()
  try { await fn(); results.push([name, 'ok', Date.now() - t0]); console.log(`✓ ${name}`) } catch (e) {
    results.push([name, 'FAIL', Date.now() - t0, e.message]); console.log(`✗ ${name}: ${e.message}`)
    for (const p of await browser.pages()) if (p !== page) await p.close().catch(() => {}) // a leftover viewer tab would stall the next specs
    await page.bringToFront().catch(() => {})
    for (const l of consoleLog.slice(-8)) console.log('    console: ' + l)
    await page.screenshot({ path: OUT + `fail-${name}.png` }).catch(() => {})
  }
}
const assert = (c, msg) => { if (!c) throw new Error(msg) }
const text = (s) => `::-p-text(${s})`
const btn = (name) => `::-p-aria([name="${name}"][role="button"])`
// click: scroll the element to the middle first, so the fixed status bar never covers it
async function click(sel, timeout = 8000) {
  const el = await page.waitForSelector(sel, { timeout, visible: true })
  await el.evaluate((e) => e.scrollIntoView({ block: 'center', inline: 'nearest' })).catch(() => {})
  await el.click()
}
// clickAgain: for lists that refresh themselves (the element may be replaced mid-click)
async function clickAgain(sel) { for (let i = 0; ; i++) { try { return await click(sel) } catch (e) { if (i >= 3 || !/detached|not clickable/.test(e.message)) throw e; await sleep(500) } } }
async function fill(label, value) {
  const el = await page.waitForSelector(`::-p-aria(${label})`, { visible: true, timeout: 8000 })
  await el.click()
  await el.evaluate((e) => e.select()) // typing then replaces whatever was prefilled
  await page.keyboard.press('Backspace')
  if (value) await el.type(value)
}
// polls from here, so background or throttled tabs cannot stall it
async function until(pg, fn, timeout = 10000, arg) {
  const end = Date.now() + timeout
  for (;;) {
    if (await pg.evaluate(fn, arg).catch(() => false)) return
    if (Date.now() > end) throw new Error('until: timed out')
    await sleep(500)
  }
}
const waitText = (s, timeout = 10000) => page.waitForFunction((x) => document.body.innerText.includes(x), { timeout }, s)
const api = (path, init = {}) => page.evaluate(async (p, i) => { const r = await fetch(p, i); return { status: r.status, body: await r.json().catch(() => null) } }, path, init)

const browser = await puppeteer.launch({ executablePath: browserPath, headless: true, acceptInsecureCerts: true, args: ['--ignore-certificate-errors', '--no-first-run'] })
const page = await browser.newPage()
const consoleLog = []
page.on('console', (m) => { if (m.type() === 'error' || m.type() === 'warn') consoleLog.push(m.text().slice(0, 300)) })
page.on('pageerror', (e) => consoleLog.push('pageerror: ' + e.message.slice(0, 300)))
page.on('response', (r) => { if (r.status() >= 400 && r.url().includes('/api/')) consoleLog.push(`${r.status()} ${r.request().method()} ${r.url().replace(BASE, '')}`) })
await page.setViewport({ width: 1280, height: 860 })
await page.evaluateOnNewDocument(() => { localStorage.setItem('berth.locale', 'zh-CN') })
const PASSWORD = 'e2e-' + Math.random().toString(36).slice(2) + '-pass'
let secret = ''

await spec('setup-login', async () => {
  await page.goto(`${BASE}/setup/${setupCode}`)
  await fill('用户名', 'e2e')
  await fill('显示名称', 'E2E')
  await fill('密码', PASSWORD)
  await fill('再输一次密码', PASSWORD)
  await click(btn('创建并登录'))
  await page.waitForFunction(() => location.pathname === '/welcome', { timeout: 10000 })
  await click(btn('下一步')) // basics
  await waitText('谁能打开面板')
  await fill('安全入口', '') // keep the e2e panel reachable without an entry path
  await click(btn('下一步')) // access
  await waitText('保护你的账号')
  await click(btn('设置验证器'))
  await waitText('用验证器 App 扫描')
  secret = await page.$eval('p.num.break-all', (e) => e.textContent.trim())
  await fill('验证码', totp(secret))
  await click(btn('确认开启'))
  await waitText('这些恢复码只显示这一次')
  await click(text('我已保存好'))
  await click(btn('完成'))
  await click('::-p-aria([name="密码 + 二步验证 输入密码后，还要验证码或 Passkey。（Passkey 单独登录也可以，它本身就包含两重验证。）"][role="radio"])')
  await sleep(500)
  await click(btn('下一步')) // security
  await waitText('已经在跑的容器')
  // untick everything: adoption is tested separately
  for (const cb of await page.$$('label input[type=checkbox]')) if (await cb.evaluate((e) => e.checked)) await cb.click()
  await click(btn('下一步'))
  // a server without the desktop gets one more step: leave it for later
  await page.waitForFunction(() => /开始使用|这台服务器的桌面/.test(document.body.innerText), { timeout: 10000 })
  if (await page.evaluate(() => document.body.innerText.includes('这台服务器的桌面'))) await click(btn('下一步'))
  await click(btn('开始使用'))
  await page.waitForFunction(() => location.pathname.endsWith('/overview'), { timeout: 10000 })
  // sign out, sign in with password + code
  await api('/api/v1/auth/logout', { method: 'POST', headers: { 'X-CSRF-Token': await page.evaluate(() => fetch('/api/v1/me').then((r) => r.json()).then((m) => m.csrf)) } })
  await page.goto(`${BASE}/login`)
  await fill('用户名', 'e2e')
  await fill('密码', PASSWORD)
  await click(btn('登录'))
  await page.waitForSelector('::-p-aria(验证码)', { timeout: 8000 })
  await fill('验证码', totp(secret, Date.now() + 30000)) // the next step: the current one was used to confirm
  await page.waitForFunction(() => location.pathname.endsWith('/overview') || location.pathname === '/welcome', { timeout: 10000 })
  // the specs below confirm every change in its preview; the quick mode has its own spec
  await page.evaluate(() => localStorage.setItem('berth.quick', '0'))
})

// Quick mode: a low-risk change that can be undone runs without the preview, and the note it
// leaves undoes it. The new page title and tab bar are there too.
await spec('quick-mode', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const D = '/srv/e2e-quick'
  onHost(`rm -rf ${D}; mkdir -p ${D}`)
  try {
    await page.evaluate(() => localStorage.setItem('berth.quick', '1'))
    await page.goto(`${BASE}/local/files${D}`)
    await waitText('新建文件夹', 10000)
    await click(btn('新建文件夹'))
    await fill('名称', 'quick-dir')
    await page.keyboard.press('Enter')
    await waitText('已完成', 15000)
    assert(onHost(`test -d ${D}/quick-dir && echo y || echo n`) === 'y', 'made without a preview')
    assert(!(await page.$('[data-testid=confirm]')), 'no preview dialog')
    await page.screenshot({ path: OUT + 'quick-mode.png' })
    await click('::-p-text(撤销)')
    await waitText('已撤销', 15000)
    for (let i = 0; i < 10 && onHost(`test -d ${D}/quick-dir && echo y || echo n`) === 'y'; i++) await sleep(1000)
    assert(onHost(`test -d ${D}/quick-dir && echo y || echo n`) === 'n', 'undone from the note')
    // a high-risk change still asks
    await page.goto(`${BASE}/local/network`)
    await waitText('防火墙规则', 10000)
    assert(await page.$('[role=tablist]'), 'the page has the tab bar')
  } finally {
    await page.evaluate(() => localStorage.setItem('berth.quick', '0'))
    onHost(`rm -rf ${D}`)
  }
})

await spec('passkey', async () => {
  const cdp = await page.createCDPSession()
  await cdp.send('WebAuthn.enable')
  await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true } })
  await page.goto(`${BASE}/account`)
  await click(btn('添加 Passkey'))
  await page.waitForFunction(() => document.body.innerText.includes('localhost ·'), { timeout: 10000 })
  await api('/api/v1/auth/logout', { method: 'POST', headers: { 'X-CSRF-Token': await page.evaluate(() => fetch('/api/v1/me').then((r) => r.json()).then((m) => m.csrf)) } })
  await page.goto(`${BASE}/login`)
  await click(btn('用 Passkey 登录'))
  await page.waitForFunction(() => location.pathname.endsWith('/overview'), { timeout: 10000 })
})

const csrf = () => page.evaluate(() => fetch('/api/v1/me').then((r) => r.json()).then((m) => m.csrf))
const state = (n) => page.evaluate(async (x) => (await fetch(`/api/v1/hosts/local/containers/${x}`).then((r) => r.json())).container.state, n)

await spec('change-undo', async () => {
  if ((await state(CONTAINER)) !== 'running') { // a previous failed run may have left it stopped
    await api(`/api/v1/hosts/local/changes/container.start`, { method: 'POST', headers: { 'X-CSRF-Token': await csrf(), 'Content-Type': 'application/json' }, body: JSON.stringify({ name: CONTAINER }) })
  }
  await page.goto(`${BASE}/local/containers/c/${CONTAINER}`)
  await click(btn('停止容器'))
  await page.waitForSelector('[data-testid=equiv]', { timeout: 10000 })
  const equiv = await page.$eval('[data-testid=equiv]', (e) => e.textContent)
  assert(equiv.includes(`berth container stop ${CONTAINER}`), 'equivalent command: ' + equiv)
  await click('[data-testid=confirm]:not([disabled])')
  await page.waitForFunction(async (n) => (await fetch(`/api/v1/hosts/local/containers/${n}`).then((r) => r.json())).container.state === 'exited', { timeout: 20000, polling: 1000 }, CONTAINER)
  await page.goto(`${BASE}/local/audit`)
  await waitText(`停止容器 ${CONTAINER}`)
  await click(btn('撤销'))
  await page.waitForFunction(async (n) => (await fetch(`/api/v1/hosts/local/containers/${n}`).then((r) => r.json())).container.state === 'running', { timeout: 20000, polling: 1000 }, CONTAINER)
})

await spec('containers', async () => {
  await page.goto(`${BASE}/local/containers/compose`)
  await waitText(PROJECT)
  // the adopt button in this project's row (Berth's own stacks are listed too)
  const row = await page.waitForSelector(`::-p-xpath(//div[contains(@class,"font-semibold")][starts-with(normalize-space(.),"${PROJECT}")]/ancestor::*[.//button][1])`)
  await (await row.$('::-p-text(纳入管理)')).click()
  await page.waitForSelector('[data-testid=confirm]', { timeout: 10000 })
  await click('[data-testid=confirm]:not([disabled])')
  await page.waitForFunction(async (n) => (await fetch('/api/v1/hosts/local/compose').then((r) => r.json())).items.find((p) => p.name === n)?.adopted, { timeout: 10000, polling: 500 }, PROJECT)
})

await spec('terminal', async () => {
  await page.goto(`${BASE}/local/overview`)
  await click(btn('终端'))
  await click(btn('在 local 上'))
  await page.waitForSelector('.xterm-helper-textarea', { timeout: 10000 })
  await sleep(1500)
  await page.focus('.xterm-helper-textarea')
  await page.keyboard.type('echo e2e-$((1+1))')
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => document.querySelector('.xterm-rows')?.textContent.includes('e2e-2'), { timeout: 10000 })
  await page.reload()
  await click(btn('终端'))
  await click(btn('接上'))
  await page.waitForFunction(() => document.querySelector('.xterm-rows')?.textContent.includes('e2e-2'), { timeout: 10000 })
  const sessions = await api('/api/v1/hosts/local/terminals')
  for (const s of sessions.body.items) await page.evaluate(async (id) => fetch(`/api/v1/hosts/local/terminals/${id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': (await fetch('/api/v1/me').then((r) => r.json())).csrf } }), s.id)
})


const post = async (path, body) => api(path, { method: 'POST', headers: { 'X-CSRF-Token': await csrf(), 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
const change = async (kind, params) => {
  const r = await post(`/api/v1/hosts/local/changes/${kind}`, params)
  const id = r.body?.id
  for (let i = 0; id && i < 120; i++) {
    const c = (await api(`/api/v1/changes/${id}`)).body
    if (!['running', 'queued', 'planned'].includes(c.status)) return c
    await sleep(1000)
  }
  return r.body
}

await spec('store-install-site', async () => {
  const name = 'e2e-tools'
  // a failed earlier run may have left it installed; the e2e panel's database is wiped each run
  sh('ssh', [HOST, `cd /srv/berth/stacks/${name} 2>/dev/null && docker compose -p ${name} down -v >/dev/null 2>&1; rm -rf /srv/berth/stacks/${name} /srv/berth/apps/${name}`])
  await page.goto(`${BASE}/local/store`)
  await fill('搜索应用', 'IT')
  await click(text('IT 工具箱'))
  await fill('实例名', name)
  await fill('端口 *', '18181')
  await sleep(800) // install checks run after a pause
  await click(btn('安装'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('安装完成，健康检查已通过', 180000)
  assert(!(await page.evaluate(() => document.body.innerText.includes('kind.'))), 'untranslated task title')
  await page.keyboard.press('Escape') // close the task drawer that opened over the dialog
  await sleep(600) // its overlay fades out
  await click(btn('为它建站点'))
  await waitText('新建站点')
  await page.waitForFunction((n) => document.body.innerText.includes(n), { timeout: 10000 }, name) // the container is preselected
  await page.keyboard.press('Escape')
  const c = await change('app.remove', { name })
  assert(c.status === 'done', 'remove: ' + c.status)
})

await spec('monitor', async () => {
  await page.goto(`${BASE}/local/monitor`)
  await waitText('一切正常')
  await click(text('可用性'))
  await click(btn('新建检查'))
  await fill('名称', 'e2e 面板')
  await fill('目标', 'https://127.0.0.1:7092/api/v1/ca.crt')
  await click(text('不校验证书（自签名证书时用）'))
  await click(btn('保存检查'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('e2e 面板', 15000)
  await page.waitForFunction(async () => (await fetch('/api/v1/uptime').then((r) => r.json())).items.some((c) => c.up === true), { timeout: 40000, polling: 2000 })
  // a personal webhook channel, tested through the panel
  await click(text('我的通知'))
  await waitText('在这个浏览器开启推送')
  const ch = await post('/api/v1/me/channels', { name: 'e2e hook', config: { kind: 'webhook', url: 'https://127.0.0.1:1/' } })
  assert(ch.status === 200, 'channel: ' + ch.status)
  const test = await post(`/api/v1/me/channels/${ch.body.id}/test`, {})
  assert(test.status === 502, 'unreachable webhook should fail visibly: ' + test.status)
  const items = (await api('/api/v1/uptime')).body.items
  for (const c of items) await change('uptime.delete', { id: c.id })
})

// a fresh connection every time: deny rules only block new connections, and fetch() would
// reuse a kept-alive one
const reach = async (port) => { try { return Number(execFileSync('curl', ['-s', '-m', '3', '-o', '/dev/null', '-w', '%{http_code}', `http://${HOST.split('@')[1]}:${port}/`]).toString()) } catch { return 0 } }

await spec('firewall', async () => {
  // the e2e panel starts with no rules; start the shared agent's firewall from nothing too
  // (the dev server's real panel keeps no firewall rules)
  sh('ssh', [HOST, 'systemctl stop berth-fw-rollback.timer 2>/dev/null; nft delete table inet berth 2>/dev/null; rm -f /var/lib/berth/firewall/boot.nft /var/lib/berth/firewall/pending*; docker rm -f e2e-fw >/dev/null 2>&1; docker run -d --name e2e-fw -p 41998:80 nginx:alpine >/dev/null'])
  await sleep(1500)
  assert((await reach(41998)) === 200, 'test port not reachable before')
  const addRule = async () => {
    await page.goto(`${BASE}/local/network`)
    await click(btn('新建规则'))
    await fill('端口', '41998')
    await fill('备注', 'e2e')
    await click(btn('保存规则（60 秒内确认）'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    await waitText('防火墙改动等待确认', 15000)
    await page.waitForFunction(() => !document.querySelector('[role=dialog]'), { timeout: 10000 }) // the rule dialog has closed
    await sleep(300)
  }
  // 1. unconfirmed change, rolled back with the button: port open again, list empty again
  await addRule()
  assert((await reach(41998)) === 0, 'deny rule did not block')
  const pe = await page.evaluate(() => [document.body.style.pointerEvents, document.querySelectorAll('[role=dialog]').length])
  assert(pe[0] !== 'none', 'page left unclickable after the dialogs closed: ' + JSON.stringify(pe))
  await click(btn('立即恢复'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await page.waitForFunction(() => !document.body.innerText.includes('防火墙改动等待确认'), { timeout: 15000 })
  assert((await reach(41998)) === 200, 'rollback did not reopen the port')
  await page.waitForFunction(async () => (await fetch('/api/v1/hosts/local/firewall').then((r) => r.json())).rules.length === 0, { timeout: 10000, polling: 1000 })
  // 2. confirmed change stays; then delete it and confirm
  await addRule()
  await click(btn('确认保留'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await page.waitForFunction(() => !document.body.innerText.includes('防火墙改动等待确认'), { timeout: 15000 })
  assert((await reach(41998)) === 0, 'confirmed rule not in force')
  const rules = (await api('/api/v1/hosts/local/firewall')).body.rules
  assert(rules.length === 1, 'rules: ' + rules.length)
  await change('firewall.rule.delete', { id: rules[0].id })
  await change('firewall.confirm', {})
  assert((await reach(41998)) === 200, 'deleting the rule did not reopen the port')
  sh('ssh', [HOST, 'docker rm -f e2e-fw >/dev/null 2>&1'])
})

await spec('bans', async () => {
  const addr = '203.0.113.77' // TEST-NET-3: never a real visitor
  const inSet = () => sh('ssh', [HOST, 'nft list set inet berth ban_v4']).includes(addr)
  await page.goto(`${BASE}/local/network`)
  await click(text('封禁'))
  await waitText('防爆破已开启')
  const c = await change('ban.add', { addr, minutes: 60, reason: 'e2e' })
  assert(c.status === 'done', 'ban: ' + c.status)
  assert(inSet(), 'address not in the nft ban set')
  await waitText(addr)
  await click(btn('解除封禁'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await page.waitForFunction((a) => !document.body.innerText.includes(a + ' ·'), { timeout: 15000 }, addr)
  assert(!inSet(), 'address still in the nft ban set after unban')
})

await spec('access-waf', async () => {
  await page.goto(`${BASE}/local/network`)
  await click(text('访问记录'))
  await waitText('在线会话（所有人）')
  await waitText('当前会话')
  await waitText('登录记录')
  // the setup run signed in with a password and a code, then with a passkey
  await page.waitForFunction(() => document.body.innerText.includes('Passkey') && document.body.innerText.includes('新地址'), { timeout: 15000 })
  await page.screenshot({ path: OUT + 'network-access.png' })
  await click('::-p-text(WAF)')
  await waitText('还没有网站开启 WAF')
  await click('::-p-text(Cloudflare)')
  await waitText('还没有 Cloudflare 账号')
})

await spec('traffic', async () => {
  const qdisc = () => sh('ssh', [HOST, `P=$(docker inspect -f '{{.State.Pid}}' ${CONTAINER}); for d in /sys/class/net/*; do [ "$(cat $d/ifindex)" = "$(cat /proc/$P/root/sys/class/net/eth0/iflink)" ] && tc qdisc show dev $(basename $d); done; true`])
  await page.goto(`${BASE}/local/network`)
  await click(text('流量'))
  await waitText('流量趋势')
  await waitText('当前入站连接')
  const c = await change('traffic.limit.set', { container: CONTAINER, down_kbit: 8000, up_kbit: 2000 })
  assert(c.status === 'done', 'limit: ' + c.status)
  const on = qdisc()
  assert(on.includes('htb') && on.includes('ingress'), 'no tc qdisc after limit: ' + on)
  await page.reload()
  await click(text('流量'))
  await waitText('限速 ↓8 Mbit/s ↑2 Mbit/s')
  await click(btn('修改限制'))
  await click(btn('取消限制'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await page.waitForFunction(() => !document.body.innerText.includes('限速 ↓8'), { timeout: 15000 })
  assert(!qdisc().includes('htb'), 'qdisc left after removing the limit')
})

await spec('wharf', async () => {
  // a fake Wharf next to the e2e panel (accepts any pairing code; /debug/state shows what Berth did)
  sh('go', ['build', '-o', 'bin/linux-arm64/fakewharf', './scripts/fakewharf'], { env: { ...process.env, PATH: '/opt/homebrew/bin:' + process.env.PATH, GOOS: 'linux', GOARCH: 'arm64', CGO_ENABLED: '0' } })
  sh('ssh', [HOST, 'pkill -f "^/tmp/fakewharf"; sleep 0.5; true'])
  sh('scp', ['-q', 'bin/linux-arm64/fakewharf', HOST + ':/tmp/fakewharf'])
  sh('ssh', [HOST, '(setsid nohup /tmp/fakewharf >/tmp/fakewharf.log 2>&1 </dev/null &) ; sleep 1; head -1 /tmp/fakewharf.log'])
  const state = () => JSON.parse(sh('ssh', [HOST, 'curl -s 127.0.0.1:41444/debug/state']))
  // a disabled site (disabled sites stay out of the shared gateway's config)
  sh('ssh', [HOST, `sqlite3 /var/lib/berth-e2e/berth.db "INSERT INTO sites(host, primary_domain, domains, enabled, target, tls, options, created_at, updated_at) VALUES('local','e2e-wharf.example.net','[\\"e2e-wharf.example.net\\"]',0,'{\\"type\\":\\"static\\"}','{\\"mode\\":\\"internal\\"}','{}',strftime('%s'),strftime('%s'))"`])
  await page.goto(`${BASE}/local/network?tab=wharf`)
  await page.waitForFunction(() => location.pathname === '/local/wharf', { timeout: 10000 })
  await waitText('三步把 Wharf 和 Berth 连起来') // the introduction, then pairing
  await waitText('github.com/wangsirco/wharf')
  await page.screenshot({ path: OUT + 'wharf-intro.png', fullPage: true })
  const url = await page.waitForSelector('input[placeholder="https://192.168.1.1:8443"]')
  await url.click()
  await url.type('https://127.0.0.1:41443')
  await click(btn('检查证书'))
  await waitText('Berth 的证书指纹')
  // Wharf's approval page is simulated: catch the redirect and come back with its code
  const req = page.waitForRequest((r) => r.url().includes('/pair/berth?'), { timeout: 10000 })
  await click(btn('前往 Wharf 授权'))
  const code = new URL((await req).url()).searchParams.get('code')
  await page.goto(`${BASE}/wharf/paired?code=${code}`)
  await page.waitForFunction(() => location.pathname === '/local/wharf', { timeout: 15000 })
  await waitText('Wharf · 家里')
  assert(state().paired === true, 'fake Wharf not paired')
  await waitText('e2e-wharf.example.net')
  const port = await page.waitForSelector('::-p-aria(外网端口)')
  await port.click()
  await port.evaluate((e) => e.select())
  await port.type('18443')
  await click(btn('公开'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('外网 18443 端口', 15000)
  const fw = Object.values(state().forwards)
  assert(fw.length === 1 && fw[0].wan_port === '18443' && fw[0].berth_ref === 'site:e2e-wharf.example.net', 'mapping at Wharf: ' + JSON.stringify(fw))
  await click(btn('撤销公开'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await page.waitForFunction(() => !document.body.innerText.includes('外网 18443 端口'), { timeout: 15000 })
  assert(Object.keys(state().forwards).length === 0, 'mapping left at Wharf')
  await page.screenshot({ path: OUT + 'network-wharf.png' })
  const c = await change('wharf.unpair', { keep: false })
  assert(c.status === 'done' && state().paired === false, 'unpair')
  sh('ssh', [HOST, 'pkill -f "^/tmp/fakewharf"; true'])
})

await spec('vms', async () => {
  // a key pair on the dev server, the public half saved in the account
  const pub = sh('ssh', [HOST, 'rm -f /tmp/e2e-vmkey /tmp/e2e-vmkey.pub; ssh-keygen -q -t ed25519 -N "" -C e2e-vm -f /tmp/e2e-vmkey; cat /tmp/e2e-vmkey.pub; incus delete -f e2e-debian 2>/dev/null; true'])
  await page.goto(`${BASE}/account`)
  await fill('粘贴公钥（例如 ~/.ssh/id_ed25519.pub 的内容）', pub.trim())
  await click(btn('添加公钥'))
  await waitText('e2e-vm')
  // the new-machine form with its defaults: Debian 13, system container, small
  await page.goto(`${BASE}/local/vms`)
  await click(btn('新建'))
  await waitText('会装好 SSH，并带上你在“账号”里保存的 1 把公钥')
  await fill('名称', 'e2e-debian')
  await click(btn('创建'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  const ip = sh('ssh', [HOST, "for i in $(seq 1 80); do a=$(incus list e2e-debian -c 4 -f csv | cut -d' ' -f1); [ -n \"$a\" ] && echo $a && break; sleep 3; done"]).trim()
  assert(/^10\.42\./.test(ip), 'no address: ' + ip)
  // cloud-init installs the SSH server; then the saved key just works
  const login = sh('ssh', [HOST, `for i in $(seq 1 60); do ssh -q -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=3 -i /tmp/e2e-vmkey root@${ip} 'echo ssh-ok; getconf PAGESIZE' 2>/dev/null && break; sleep 3; done; true`])
  assert(login.includes('ssh-ok'), 'SSH with the saved key failed: ' + login)
  // details: terminal into it, snapshot, restore
  // creating leaves the task log open (and opens the new machine's details): close them all
  for (let k = 0; k < 4 && (await page.$$('[role=dialog]')).length > 0; k++) {
    await page.keyboard.press('Escape')
    await sleep(400)
  }
  await page.waitForFunction((a) => document.body.innerText.includes(a), { timeout: 20000 }, ip) // the list picks the address up by itself
  await click(`::-p-xpath(//div[contains(@class,"font-semibold")][starts-with(normalize-space(.),"e2e-debian ")])`)
  await waitText('ssh -J root@')
  await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="终端"])') // the drawer's, not the rows' behind it
  await page.waitForSelector('.xterm-helper-textarea', { timeout: 15000 })
  await sleep(1500)
  await page.focus('.xterm-helper-textarea')
  await page.keyboard.type('hostname')
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => (document.querySelector('.xterm-rows')?.textContent ?? '').includes('e2e-debian'), { timeout: 15000 })
  await page.keyboard.press('Escape')
  // acceptance: a site for the machine — a web page inside it, served by the edge over HTTPS
  // (the dev server's real panel has no sites, so this panel's edge config replaces nothing)
  sh('ssh', [HOST, `incus exec e2e-debian -- sh -c 'mkdir -p /tmp/w && echo vm-site-ok > /tmp/w/index.html && cd /tmp/w && (setsid nohup python3 -m http.server 80 >/dev/null 2>&1 &)'`])
  const site = await change('site.create', { domains: ['vm-e2e.berth.test'], tls: { mode: 'internal' }, target: { type: 'address', upstreams: [ip + ':80'] } })
  assert(site.status === 'done', 'site for the VM: ' + JSON.stringify(site.result ?? site))
  let page1 = ''
  for (let i = 0; i < 20 && !page1.includes('vm-site-ok'); i++) {
    page1 = sh('ssh', [HOST, 'curl -sk -m 5 --resolve vm-e2e.berth.test:443:127.0.0.1 https://vm-e2e.berth.test/; true'])
    if (!page1.includes('vm-site-ok')) await sleep(1500)
  }
  assert(page1.includes('vm-site-ok'), 'the site does not reach the VM: ' + page1)
  assert((await change('site.delete', { id: site.result.id })).status === 'done', 'site delete')
  const snap = await change('vm.snapshot', { name: 'e2e-debian', snapshot: 'e2e-snap' })
  assert(snap.status === 'done', 'snapshot')
  const rest = await change('vm.restore', { name: 'e2e-debian', snapshot: 'e2e-snap' })
  assert(rest.status === 'done' && rest.result.safety_snapshot, 'restore: ' + JSON.stringify(rest.result))
  await page.screenshot({ path: OUT + 'vms.png' })
  const del = await change('vm.delete', { name: 'e2e-debian' })
  assert(del.status === 'done', 'delete')
  assert(!sh('ssh', [HOST, 'incus list -c n -f csv']).includes('e2e-debian'), 'still there')
})

await spec('remote-desktop', async () => {
  // the dev server keeps an Xfce desktop with xrdp in the t-rdp container; give its test user a fresh password
  const pw = 'e2e-' + Math.random().toString(36).slice(2, 12)
  const ip = sh('ssh', [HOST, `incus exec t-rdp -- sh -c 'echo rdptest:${pw} | chpasswd' && incus list t-rdp -c 4 -f csv | cut -d' ' -f1`]).trim()
  assert(/^10\./.test(ip), 't-rdp has no address: ' + ip)
  await page.goto(`${BASE}/local/remote`)
  await waitText('还没有远程连接')
  if ((await page.content()).includes('还没装远程桌面组件')) {
    await click(btn('安装'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    await page.waitForFunction(() => !document.body.innerText.includes('还没装远程桌面组件'), { timeout: 180000 })
    for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
  }
  await click(btn('新建连接'))
  await fill('地址', ip)
  await fill('名称', 'e2e-xfce')
  await fill('用户名', 'rdptest')
  await fill('密码', pw)
  const opened = browser.waitForTarget((t) => t.url().includes('/remote/'), { timeout: 30000 })
  await click(btn('保存并连接'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  const viewer = await (await opened).page()
  await viewer.bringToFront() // background tabs get no animation frames, which waits poll on
  await viewer.setViewport({ width: 1280, height: 800 })
  // xrdp starts (or rejoins) an Xfce session: usually a few seconds, now and then much longer
  await until(viewer, () => document.body.innerText.includes('已连接'), 60000)
    .catch(async (e) => { await viewer.screenshot({ path: OUT + 'remote-desktop-fail.png' }); throw new Error('viewer never connected: ' + e.message) })
  // the desktop is drawn: the display layers have size and the picture is not one flat colour
  await sleep(6000)
  await viewer.screenshot({ path: OUT + 'remote-desktop.png' })
  const shot = await viewer.screenshot({ encoding: 'base64' })
  assert(shot.length > 20000, 'remote picture looks empty: ' + shot.length)
  const size = await viewer.evaluate(() => { const c = document.querySelector('canvas'); return c ? [c.width, c.height] : [0, 0] })
  assert(size[0] >= 640 && size[1] >= 400, 'display size ' + size)
  await viewer.close()
  await page.bringToFront()
  // the audit log knows who opened what
  const audit = await page.evaluate(async () => (await fetch('/api/v1/changes?kind=rd.open').then((r) => r.json())).items)
  assert(audit?.length > 0, 'no rd.open audit entry')
  const conns = await page.evaluate(async () => (await fetch('/api/v1/remote').then((r) => r.json())).items)
  const del = await change('rd.connection.delete', { id: conns.find((c) => c.name === 'e2e-xfce').id })
  assert(del.status === 'done', 'delete')
})

await spec('server-desktop', async () => {
  // Xfce + TigerVNC are on the dev server; start from "off"
  await change('desktop.disable', {})
  await page.goto(`${BASE}/local/remote`)
  await waitText('这台服务器的桌面')
  await waitText('桌面已关闭', 15000)
  const opened = browser.waitForTarget((t) => t.url().includes('/remote/'), { timeout: 900000 })
  await click(btn('开启并打开'))
  // without a browser on the desktop, the panel offers the everyday software first
  await sleep(800)
  if ((await page.content()).includes('桌面里还没有浏览器等常用软件')) {
    await waitText('Firefox 浏览器')
    await waitText('还要下载约')
    await click(btn('安装并打开'))
  }
  await click('[data-testid=confirm]:not([disabled])', 15000)
  const viewer = await (await opened).page()
  await viewer.bringToFront() // background tabs get no animation frames, which waits poll on
  await viewer.setViewport({ width: 1280, height: 800 })
  await until(viewer, () => document.body.innerText.includes('已连接'), 60000)
  await sleep(6000)
  // the side panel: server load, then a shortcut key and "type text" really reach the desktop
  await until(viewer, () => document.body.innerText.includes('服务器负载') && /\d+%/.test(document.body.innerText), 20000)
  sh('ssh', [HOST, 'rm -f /tmp/berth-e2e-typed'])
  await (await viewer.waitForSelector('::-p-xpath(//aside//button[.//div[normalize-space(.)="Ctrl+Alt+T"]])')).click()
  await sleep(3000)
  await (await viewer.waitForSelector('::-p-xpath(//aside//section[.//div[normalize-space(.)="粘贴为键入"]]//textarea)')).type('echo berth-typed > /tmp/berth-e2e-typed\n')
  await (await viewer.waitForSelector('::-p-xpath(//aside//button[normalize-space(.)="键入"])')).click()
  await sleep(2500)
  await viewer.screenshot({ path: OUT + 'server-desktop.png' })
  assert(sh('ssh', [HOST, 'cat /tmp/berth-e2e-typed 2>/dev/null; true']).includes('berth-typed'), 'typed command did not run on the desktop')
  await viewer.close()
  await page.bringToFront()
  assert(!sh('ssh', [HOST, 'rpm -q firefox mousepad; true']).includes('not installed'), 'basic software missing')
  // VNC only on loopback
  const ss = sh('ssh', [HOST, 'ss -Hltn "sport = :5909"'])
  assert(ss.includes('127.0.0.1:5909') && !ss.includes('0.0.0.0:5909') && !ss.includes('*:5909'), 'listens beyond loopback: ' + ss)
  for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
  await page.reload()
  await waitText('桌面运行中', 15000)
  await click(btn('关闭'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('桌面已关闭', 30000)
  assert(!sh('ssh', [HOST, 'ss -Hltn "sport = :5909"; true']).includes('5909'), 'still listening')
})

await spec('store-lan', async () => {
  const name = 'e2e-lan'
  sh('ssh', [HOST, `cd /srv/berth/stacks/${name} 2>/dev/null && docker compose -p ${name} down -v >/dev/null 2>&1; rm -rf /srv/berth/stacks/${name} /srv/berth/apps/${name}`])
  await page.goto(`${BASE}/local/store`)
  await fill('搜索应用', 'IT')
  await click(text('IT 工具箱'))
  await fill('实例名', name)
  await fill('端口 *', '18282')
  await click(text('内网'))
  await waitText('http://192.0.2.10:18282')
  await sleep(800)
  await click(btn('安装'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('打开 192.0.2.10:18282', 180000)
  const code = sh('curl', ['-s', '-o', '/dev/null', '-w', '%{http_code}', '--max-time', '5', 'http://192.0.2.10:18282/'])
  assert(code === '200', 'not reachable on the LAN: ' + code)
  await page.screenshot({ path: OUT + 'store-lan.png' })
  for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
  await page.goto(`${BASE}/local/containers`)
  await waitText('端口对所有地址开放')
  await page.screenshot({ path: OUT + 'containers-exposure.png' })
  const rm = await change('app.remove', { name })
  assert(rm.status === 'done', 'remove')
  sh('ssh', [HOST, `rm -rf /srv/berth/apps/${name}`]) // removing an app keeps its data; the next run starts fresh
})

await spec('rustdesk-guide', async () => {
  const name = 'e2e-rustdesk'
  sh('ssh', [HOST, `cd /srv/berth/stacks/${name} 2>/dev/null && docker compose -p ${name} down -v >/dev/null 2>&1; rm -rf /srv/berth/stacks/${name} /srv/berth/apps/${name}`])
  const inst = await change('app.install', { app: 'rustdesk-server', name })
  assert(inst.status === 'done', 'install: ' + inst.status + ' ' + (inst.log ?? '').slice(-300))
  // the relay answers on its ports
  assert(Number(sh('ssh', [HOST, 'ss -Hltn | grep -cE ":2111[567] " ; true']).trim()) >= 3, 'relay ports not listening')
  await page.goto(`${BASE}/local/store/installed`)
  await click(btn('使用说明'))
  await waitText('在 RustDesk 客户端里填')
  await waitText('ID 服务器')
  // the key is written by hbbs on its first start: reopen until it shows
  for (let i = 0; i < 10 && !(await page.content()).match(/[A-Za-z0-9+/]{40,}=/); i++) {
    await page.keyboard.press('Escape'); await sleep(1500)
    await click(btn('使用说明'))
    await waitText('ID 服务器')
  }
  const key = sh('ssh', [HOST, `cat /srv/berth/apps/${name}/data/id_ed25519.pub`]).trim()
  await waitText(key)
  await waitText('21115/tcp')
  await waitText('21116/udp')
  // a device signing up lands in hbbs's peer table; put one there and reopen
  sh('ssh', [HOST, `python3 -c "import sqlite3,os;c=sqlite3.connect('/srv/berth/apps/${name}/data/db_v2.sqlite3');c.execute('insert into peer(guid,id,uuid,pk,info) values(?,?,?,?,?)',(os.urandom(16),'123456789',b'u',b'k','{\\"ip\\":\\"::ffff:10.0.2.50\\"}'));c.commit()"`])
  await page.keyboard.press('Escape'); await sleep(800)
  await click(btn('使用说明'))
  await waitText('已登记的设备（1）')
  await waitText('10.0.2.50')
  await page.screenshot({ path: OUT + 'rustdesk-guide.png' })
  await page.keyboard.press('Escape')
  const rm = await change('app.remove', { name })
  assert(rm.status === 'done', 'remove')
  sh('ssh', [HOST, `rm -rf /srv/berth/apps/${name}`]) // its keys and data stay after removal; the next run starts fresh
})

await spec('flows', async () => {
  // t-lxc on the dev server makes HTTPS requests; within a few one-minute samples the panel
  // knows where they went. A fresh server has no t-lxc: make a throwaway one, removed after.
  const made = sh('ssh', [HOST, 'incus info t-lxc >/dev/null 2>&1 && echo have || (incus launch images:debian/13/cloud t-lxc -c limits.memory=256MiB >/dev/null 2>&1 && sleep 15 && incus exec t-lxc -- sh -c "command -v curl >/dev/null || (apt-get -qq update && apt-get -qq install -y curl)" >/dev/null 2>&1; echo made)']) === 'made'
  try {
  let seen = null
  for (let i = 0; i < 18 && !seen; i++) {
    sh('ssh', [HOST, 'incus exec t-lxc -- curl -s -o /dev/null --max-time 5 https://www.cloudflare.com; true'])
    await sleep(10000)
    const r = await page.evaluate(async () => (await fetch('/api/v1/hosts/local/flows?obj=vm:t-lxc&range=1h').then((r) => r.json())))
    seen = r.out?.find((p) => p.ports.includes('443/tcp') && p.bytes > 0)
  }
  assert(seen, 'no outbound flow from t-lxc recorded')
  await page.goto(`${BASE}/local/network?tab=traffic`)
  await waitText('谁在和外面通信')
  await click(text('t-lxc'))
  await waitText('出站去向')
  await waitText(seen.peer)
  await page.screenshot({ path: OUT + 'flows.png' })
  await page.keyboard.press('Escape')
  } finally {
    if (made) sh('ssh', [HOST, 'incus delete -f t-lxc; true'])
  }
})

await spec('system', async () => {
  await page.goto(`${BASE}/local/system`)
  await waitText('Apple Mac mini')
  for (const [tab, want] of [['服务', 'NetworkManager'], ['进程', 'PID'], ['磁盘', 'nvme0n1'], ['更新', '重新检查']]) {
    await click(`::-p-xpath(//button[normalize-space(.)="${tab}"])`)
    await waitText(want, 20000)
  }
  // a NIC change through the dialog, kept on the banner
  await click('::-p-xpath(//button[normalize-space(.)="网卡"])')
  await waitText('end0')
  await page.screenshot({ path: OUT + 'system-nics.png' })
  await click('::-p-xpath(//section[.//b[normalize-space(.)="end0"]]//button[normalize-space(.)="设置"])')
  const dnsInput = await page.waitForSelector('::-p-xpath(//*[@role="dialog"]//input[@placeholder="223.5.5.5 1.1.1.1"])', { visible: true })
  await dnsInput.click({ clickCount: 3 })
  await dnsInput.type('1.1.1.1')
  await click(btn('应用'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('能看到这句话说明网络还通', 60000)
  for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
  await click(btn('保留'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await page.waitForFunction(() => !document.body.innerText.includes('能看到这句话说明网络还通'), { timeout: 20000 })
  assert(sh('ssh', [HOST, 'nmcli -g ipv4.dns connection show "Wired connection 1"']).trim() === '1.1.1.1', 'DNS not kept')
  // put it back
  const back = await change('system.nic.set', { name: 'end0', dns4: [], seconds: 60 })
  assert(back.status === 'done', 'restore dns')
  const ok = await change('system.net.confirm', {})
  assert(ok.status === 'done', 'confirm restore')
  assert(sh('ssh', [HOST, 'nmcli -g ipv4.dns connection show "Wired connection 1"']).trim() === '', 'DNS not restored')
})

await spec('system-admin', async () => {
  const waitChange = async (id) => { for (let i = 0; i < 180; i++) { const c = (await api(`/api/v1/changes/${id}`)).body; if (!['running', 'queued', 'planned'].includes(c.status)) return c; await sleep(1000) } }
  const revert = async (c) => { const r = await post(`/api/v1/changes/${c.id}/revert`, {}); assert(r.status === 200, 'revert ' + c.kind + ': ' + JSON.stringify(r.body)); return waitChange(r.body.id) }
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  // ---- scheduled jobs: one made in the editor, run now, output and exit code recorded ----
  await page.goto(`${BASE}/local/system?tab=jobs`)
  await page.waitForFunction(() => location.pathname === '/local/cron', { timeout: 10000 })
  await click(btn('新建任务'))
  await fill('名称', 'e2e 任务')
  await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="每周"])')
  await page.waitForFunction(() => document.querySelector('[role=dialog]')?.innerText.includes('接下来'), { timeout: 8000 })
  const ta = await page.waitForSelector('::-p-xpath(//*[@role="dialog"]//textarea)', { visible: true })
  await ta.type('echo hello-from-job; id -un; exit 3')
  await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"])')
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('下次', 15000)
  const jobs = (await api('/api/v1/hosts/local/jobs')).body
  const job = jobs.items.find((j) => j.name === 'e2e 任务')
  assert(job && /^0 3 \* \* 1$/.test(job.cron) && job.next > Date.now() / 1000, 'job saved with a weekly schedule: ' + JSON.stringify(job))
  assert(jobs.system && jobs.system.entries.some((e) => e.source === '/etc/cron.d/0hourly'), 'system crontab listed')
  const runC = await change('cron.run', { id: job.id })
  assert(runC.status === 'done', 'run now')
  let runs
  for (let i = 0; i < 30; i++) { runs = (await api(`/api/v1/hosts/local/jobs/${job.id}/runs`)).body.items; if (runs[0] && runs[0].status !== 'running') break; await sleep(1000) }
  assert(runs[0].status === 'failed' && runs[0].code === 3 && runs[0].output.includes('hello-from-job') && runs[0].output.includes('root'), 'run recorded: ' + JSON.stringify(runs[0]))
  // the failure raised an alert; deleting the job clears it, and undo brings the job back
  const del = await change('cron.delete', { id: job.id })
  assert(del.status === 'done', 'delete job')
  await revert(del)
  assert((await api('/api/v1/hosts/local/jobs')).body.items.some((j) => j.id === job.id), 'undo delete keeps the id')
  // a Berth action as the person who made it
  const act = await change('cron.set', { name: 'e2e 操作', cron: '@monthly', action: { type: 'change', kind: 'uptime.delete', params: { id: 987654 } } })
  assert(act.status === 'done', 'action job')
  await change('cron.run', { id: act.result.id })
  for (let i = 0; i < 20; i++) { runs = (await api(`/api/v1/hosts/local/jobs/${act.result.id}/runs`)).body.items; if (runs[0] && runs[0].status !== 'running') break; await sleep(500) }
  assert(runs[0].status === 'failed' && /uptime|检查/.test(runs[0].output), 'action run recorded: ' + JSON.stringify(runs[0]))
  for (const id of [job.id, act.result.id]) await change('cron.delete', { id })

  // ---- SSH: a change rolls back by itself unless kept ----
  const eff = () => onHost('/usr/sbin/sshd -T | grep -i "^passwordauthentication "').toLowerCase()
  assert(eff() === 'passwordauthentication yes', 'password login on before')
  await page.goto(`${BASE}/local/system?tab=access`)
  await waitText('监听 22 端口', 15000)
  const off = await change('ssh.set', { password_auth: false, root_login: 'yes', seconds: 30 })
  assert(off.status === 'done', 'ssh.set: ' + JSON.stringify(off.result))
  assert(eff() === 'passwordauthentication no', 'password login off')
  await waitText('秒后自动恢复', 15000)
  await sleep(36000)
  assert(eff() === 'passwordauthentication yes', 'rolled back by itself')
  assert(onHost('test -e /etc/ssh/sshd_config.d/00-berth-panel.conf && echo left || true') === '', 'drop-in removed by the rollback')
  const off2 = await change('ssh.set', { password_auth: false, root_login: 'yes', seconds: 60 })
  assert((await change('ssh.confirm', {})).status === 'done' && eff() === 'passwordauthentication no', 'kept')
  const back = await revert(off2) // undo is again under a rollback timer
  assert(eff() === 'passwordauthentication yes', 'undo puts it back')
  await change('ssh.confirm', {})
  assert(onHost('systemctl is-active berth-ssh-rollback.timer || true') !== 'active', 'no timer left')
  assert(onHost('test -e /etc/ssh/sshd_config.d/00-berth-panel.conf && echo left || true') === '', 'undo removed the drop-in Berth added')
  assert(back.status === 'done', 'undo done')

  // ---- system users, groups and keys ----
  const pub = onHost('rm -f /tmp/e2e-k /tmp/e2e-k.pub; ssh-keygen -q -t ed25519 -N "" -C e2e-key -f /tmp/e2e-k && cat /tmp/e2e-k.pub')
  assert((await change('sysgroup.create', { name: 'e2egrp' })).status === 'done', 'group')
  const cu = await change('sysuser.create', { name: 'e2euser', gecos: 'E2E User' })
  assert(cu.status === 'done' && onHost('id -un e2euser') === 'e2euser', 'user created')
  assert((await change('ssh.key_add', { user: 'e2euser', key: pub })).status === 'done', 'key added')
  assert(onHost('ssh -i /tmp/e2e-k -o StrictHostKeyChecking=no -o BatchMode=yes e2euser@127.0.0.1 id -un') === 'e2euser', 'key login works')
  assert((await change('sysuser.update', { name: 'e2euser', groups: ['e2egrp'] })).status === 'done' && onHost('id -nG e2euser').includes('e2egrp'), 'groups')
  const lk = await change('sysuser.update', { name: 'e2euser', locked: true })
  const locked = async () => (await api('/api/v1/hosts/local/system/accounts')).body.users.find((u) => u.name === 'e2euser')?.locked
  assert(lk.status === 'done' && await locked() === true, 'locked')
  await page.reload()
  await waitText('e2euser', 15000)
  await waitText('已锁定')
  await revert(lk)
  assert(await locked() === false, 'unlocked by undo')
  const sys = await post('/api/v1/hosts/local/changes/sysuser.delete', { name: 'bin', remove_home: false })
  assert(sys.status === 409, 'system account refused: ' + sys.status)
  assert((await change('sysuser.delete', { name: 'e2euser', remove_home: true })).status === 'done', 'user deleted')
  assert((await change('sysgroup.delete', { name: 'e2egrp' })).status === 'done', 'group deleted')
  assert(onHost('id e2euser 2>/dev/null || echo gone; test -d /home/e2euser && echo home || true') === 'gone', 'user and home gone')
  onHost('rm -f /tmp/e2e-k /tmp/e2e-k.pub')

  // ---- kernel parameter, swap file, resolver ----
  await page.goto(`${BASE}/local/system?tab=kernel`)
  await waitText('内存映射数上限', 15000)
  const sc = await change('sysctl.set', { values: { 'vm.max_map_count': '262144' } })
  assert(sc.status === 'done' && onHost('sysctl -n vm.max_map_count') === '262144' && onHost('cat /etc/sysctl.d/90-berth.conf').includes('vm.max_map_count = 262144'), 'sysctl applied and kept')
  await revert(sc)
  assert(onHost('sysctl -n vm.max_map_count') === '1048576' && onHost('test -e /etc/sysctl.d/90-berth.conf && echo y || true') === '', 'sysctl undone')
  const sw = await change('swap.set', { file_mb: 512 })
  assert(sw.status === 'done' && onHost('swapon --show=NAME --noheadings').includes('/var/swap/berth.swap') && onHost('grep -c berth.swap /etc/fstab') === '1', 'swap file on')
  await revert(sw)
  assert(!onHost('swapon --show=NAME --noheadings').includes('berth.swap') && onHost('grep -c berth.swap /etc/fstab || true') === '0', 'swap file removed')
  const rs = await change('dns.resolver', { servers: ['9.9.9.9'] })
  assert(rs.status === 'done' && onHost('resolvectl dns | head -1').includes('9.9.9.9'), 'resolver set')
  await revert(rs)
  assert(!onHost('resolvectl dns | head -1').includes('9.9.9.9'), 'resolver undone')
  // automatic updates: on in a window, then off again
  const au = await change('system.auto_updates', { enabled: true, type: 'security', time: '04:30' })
  assert(au.status === 'done', 'auto updates: ' + JSON.stringify(au.result))
  assert(onHost('systemctl is-enabled dnf5-automatic.timer') === 'enabled' && onHost('grep upgrade_type /etc/dnf/automatic.conf').includes('security'), 'auto updates on')
  assert(onHost('systemctl show -p TimersCalendar dnf5-automatic.timer').includes('04:30'), 'window set')
  await revert(au)
  assert(onHost('systemctl is-enabled dnf5-automatic.timer || true') !== 'enabled', 'auto updates off again')
})

await spec('storage', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitChange = async (id) => { for (let i = 0; i < 180; i++) { const c = (await api(`/api/v1/changes/${id}`)).body; if (!['running', 'queued', 'planned'].includes(c.status)) return c; await sleep(1000) } }
  const loops = onHost('for i in 1 2; do truncate -s 300M /var/tmp/e2e-disk$i.img; losetup -f --show /var/tmp/e2e-disk$i.img; done').split('\n')
  try {
    await page.goto(`${BASE}/local/system?tab=disks`)
    await waitText(loops[0].replace('/dev/', ''), 15000)
    await waitText('系统盘')
    // the system disk is refused, whatever the caller says
    const sys = (await api('/api/v1/hosts/local/system/storage')).body.disks.find((d) => d.system)
    const refused = await post('/api/v1/hosts/local/changes/disk.format', { device: sys.path, fstype: 'ext4' })
    assert(refused.status === 409, 'system disk format refused: ' + refused.status)
    const md = await change('raid.create', { kind: 'md', name: 'e2er', level: 'raid1', devices: loops })
    assert(md.status === 'done', 'raid: ' + JSON.stringify(md.result))
    assert(/^md\d+ : active raid1/m.test(onHost('cat /proc/mdstat')), 'array running')
    assert((await change('disk.format', { device: '/dev/md/e2er', fstype: 'ext4', label: 'e2e' })).status === 'done', 'format')
    const mt = await change('disk.mount', { device: '/dev/md/e2er', path: '/mnt/e2e-raid' })
    assert(mt.status === 'done', 'mount: ' + JSON.stringify(mt.result))
    assert(onHost('findmnt -no FSTYPE /mnt/e2e-raid') === 'ext4' && onHost('grep "/mnt/e2e-raid" /etc/fstab').includes('nofail 0 2 # berth'), 'mounted and in fstab')
    onHost('head -c 70M /dev/urandom > /mnt/e2e-raid/big.bin')
    const sp = (await api('/api/v1/hosts/local/system/space?path=/mnt/e2e-raid&refresh=1')).body
    assert(sp.big.some((f) => f.path === '/mnt/e2e-raid/big.bin'), 'space finds the big file')
    await page.reload()
    await waitText('RAID 阵列', 15000)
    await waitText('e2er')
    // a member drops out and comes back
    const rm = await change('raid.repair', { array: '/dev/md/e2er', op: 'remove', device: loops[1] })
    assert(rm.status === 'done' && /_/.test(onHost('cat /proc/mdstat')), 'degraded after removing a member')
    assert((await change('raid.repair', { array: '/dev/md/e2er', op: 'add', device: loops[1] })).status === 'done', 'member added back')
    assert((await change('raid.scrub', { target: '/dev/md/e2er' })).status === 'done', 'scrub')
    const un = await post(`/api/v1/changes/${mt.id}/revert`, {})
    assert((await waitChange(un.body.id)).status === 'done' && onHost('grep -c "/mnt/e2e-raid" /etc/fstab || true') === '0', 'undo mount drops the fstab line')
    const del = await change('raid.delete', { array: '/dev/md/e2er' })
    assert(del.status === 'done' && !/^md\d+ :/m.test(onHost('cat /proc/mdstat')), 'array deleted: ' + del.status)
    assert(onHost('test -e /etc/mdadm.conf && echo y || true') === '', 'mdadm.conf removed again')
    const cl = (await api('/api/v1/hosts/local/system/cleanup')).body.items
    assert(cl.some((c) => c.key === 'journal') && cl.some((c) => c.key === 'images'), 'clean-up suggestions')
  } finally {
    onHost(`umount /mnt/e2e-raid 2>/dev/null; sed -i '\\#/mnt/e2e-raid#d' /etc/fstab; mdadm --stop /dev/md/e2er 2>/dev/null; for l in ${loops.join(' ')}; do mdadm --zero-superblock $l 2>/dev/null; losetup -d $l; done; rm -f /var/tmp/e2e-disk*.img; rmdir /mnt/e2e-raid 2>/dev/null; true`)
  }
})

await spec('drift', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const F = '/etc/sysctl.d/90-berth.conf'
  const drifts = async () => (await api('/api/v1/hosts/local/drift')).body.items.filter((d) => d.path === F)
  assert((await change('sysctl.set', { values: { 'vm.max_map_count': '262144' } })).status === 'done', 'sysctl set')
  try {
    assert((await drifts()).length === 0, 'no drift right after Berth wrote it')
    onHost(`echo '# edited by hand' >> ${F}`)
    const d1 = await drifts()
    assert(d1.length === 1 && d1[0].state === 'changed' && d1[0].kind === 'sysctl', 'drift noticed: ' + JSON.stringify(d1))
    await page.goto(`${BASE}/local/system`)
    await waitText('在面板以外被改过', 15000)
    const plan = await post('/api/v1/hosts/local/changes/drift.restore?plan=1', { path: F })
    assert(plan.body.plan.files[0].unified.includes('-# edited by hand'), 'restore preview shows the diff')
    assert((await change('drift.restore', { path: F })).status === 'done', 'restore')
    assert(!onHost(`cat ${F}`).includes('edited by hand') && onHost(`cat ${F}.external`).includes('edited by hand'), 'Berth version back, edit kept aside')
    assert((await drifts()).length === 0, 'no drift after restore')
    onHost(`echo '# edited again' >> ${F}`)
    assert((await drifts()).length === 1, 'second edit noticed')
    assert((await change('drift.accept', { path: F })).status === 'done', 'accept')
    assert((await drifts()).length === 0 && onHost(`cat ${F}`).includes('edited again'), 'accepted as is')
  } finally {
    await change('sysctl.set', { values: { 'vm.max_map_count': '' }, runtime: { 'vm.max_map_count': '1048576' } })
    onHost(`rm -f ${F} ${F}.external; sysctl -qw vm.max_map_count=1048576`)
  }
})

await spec('user-traffic-quota', async () => {
  const pw = 'e2e-q-' + Math.random().toString(36).slice(2)
  const cu = await post('/api/v1/users', { username: 'e2equota', display_name: 'Quota', role: 'admin', perms: ['containers.manage'], password: pw })
  assert(cu.status === 200, 'user: ' + JSON.stringify(cu.body))
  const id = (await api('/api/v1/users')).body.items.find((u) => u.username === 'e2equota').id
  const q = await change('user.quota', { id, quota: { traffic_gb: 5, sites: 2 } })
  assert(q.status === 'done', 'quota saved: ' + JSON.stringify(q).slice(0, 300))
  const u = (await api(`/api/v1/users/${id}/usage`)).body
  assert(u.quota.traffic_gb === 5 && typeof u.usage.traffic_bytes === 'number', 'usage carries traffic: ' + JSON.stringify(u))
  await page.goto(`${BASE}/local/users`)
  await click('::-p-text(e2equota)')
  await waitText('本月流量（GB）', 10000)
  const v = await page.evaluate(() => [...document.querySelectorAll('input[type=number]')].map((e) => e.value))
  assert(v.includes('5'), 'the field shows 5: ' + JSON.stringify(v))
  // a mistyped host shows a notice instead of a broken page
  await page.goto(`${BASE}/no-such-host/overview`)
  await waitText('没有叫 no-such-host 的主机', 10000)
})

await spec('approvals', async () => {
  const put = async (path, body) => api(path, { method: 'PUT', headers: { 'X-CSRF-Token': await csrf(), 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  assert((await put('/api/v1/settings', { values: { 'approval.who': 'admins', 'approval.kinds': 'uptime.*', 'approval.hours': '24' } })).status === 200, 'policy saved')
  const pw = 'e2e-ops-' + Math.random().toString(36).slice(2)
  const cu = await post('/api/v1/users', { username: 'e2eops', display_name: 'Ops', role: 'admin', perms: ['monitor.manage', 'overview.view'], password: pw })
  assert(cu.status === 200, 'admin user: ' + JSON.stringify(cu.body))
  const ctx2 = await browser.createBrowserContext()
  try {
    const p2 = await ctx2.newPage()
    await p2.goto(`${BASE}/login`)
    const req = await p2.evaluate(async (pw) => {
      const l = await fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: 'e2eops', password: pw }) }).then((r) => r.json())
      const r = await fetch('/api/v1/hosts/local/changes/uptime.set', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': l.csrf },
        body: JSON.stringify({ name: 'e2e 审批', kind: 'tcp', target: '127.0.0.1:7092' }) })
      return r.json()
    }, pw)
    assert(req.status === 'pending_approval', 'admin change waits: ' + JSON.stringify(req).slice(0, 200))
    // the super administrator sees it and approves it in the audit page
    await page.goto(`${BASE}/local/audit`)
    await waitText('待批准', 15000)
    await click(btn('批准执行'))
    for (let i = 0; i < 20; i++) { if ((await api(`/api/v1/changes/${req.id}`)).body.status === 'done') break; await sleep(500) }
    const done = (await api(`/api/v1/changes/${req.id}`)).body
    assert(done.status === 'done' && done.log.includes('confirmed by e2e'), 'approved: ' + done.status)
    // a second request is rejected
    const cid = (await api('/api/v1/uptime')).body.items.find((c) => c.name === 'e2e 审批').id
    const req2 = await p2.evaluate(async (cid) => {
      const me = await fetch('/api/v1/me').then((r) => r.json())
      return fetch('/api/v1/hosts/local/changes/uptime.delete', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': me.csrf }, body: JSON.stringify({ id: cid }) }).then((r) => r.json())
    }, cid)
    assert(req2.status === 'pending_approval', 'second waits')
    assert((await post(`/api/v1/changes/${req2.id}/reject`, {})).status === 200, 'reject')
    assert((await api(`/api/v1/changes/${req2.id}`)).body.status === 'rejected', 'rejected')
    // the admin cannot approve their own request
    const req3 = await p2.evaluate(async (cid) => {
      const me = await fetch('/api/v1/me').then((r) => r.json())
      const r = await fetch('/api/v1/hosts/local/changes/uptime.delete', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': me.csrf }, body: JSON.stringify({ id: cid }) }).then((r) => r.json())
      const c = await fetch(`/api/v1/changes/${r.id}/confirm`, { method: 'POST', headers: { 'X-CSRF-Token': me.csrf } })
      return { id: r.id, code: c.status }
    }, cid)
    assert(req3.code === 403, 'self-approval refused: ' + req3.code)
    await post(`/api/v1/changes/${req3.id}/reject`, {})
  } finally {
    await ctx2.close()
    await put('/api/v1/settings', { values: { 'approval.who': '', 'approval.kinds': '' } })
    const checks = (await api('/api/v1/uptime')).body.items.filter((c) => c.name === 'e2e 审批')
    for (const c of checks) await change('uptime.delete', { id: c.id })
  }
})

await spec('simple-screen-share', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const site = await change('site.create', { domains: ['wall-e2e.berth.test'], tls: { mode: 'internal' }, target: { type: 'address', upstreams: ['127.0.0.1:18080'] } })
  assert(site.status === 'done', 'site: ' + JSON.stringify(site.result))
  const pw = 'e2e-fam-' + Math.random().toString(36).slice(2)
  const fam = await post('/api/v1/users', { username: 'e2efamily', display_name: 'Family', role: 'viewer', perms: [], password: pw, simple: true })
  assert(fam.status === 200, 'family user')
  const ctx2 = await browser.createBrowserContext()
  try {
    // the family account lands on the app wall and cannot reach the panel
    const p2 = await ctx2.newPage()
    await p2.goto(`${BASE}/login`)
    await p2.evaluate(async (pw) => fetch('/api/v1/auth/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: 'e2efamily', password: pw }) }), pw)
    await p2.goto(`${BASE}/`)
    await p2.waitForFunction(() => location.pathname === '/wall' && document.querySelectorAll('[data-wall-tile]').length > 0, { timeout: 15000 })
    assert(await p2.evaluate(() => [...document.querySelectorAll('[data-wall-tile]')].some((a) => a.getAttribute('href') === 'https://wall-e2e.berth.test')), 'tile for the site')
    assert(await p2.evaluate(() => !document.body.innerText.includes('进入面板')), 'no way into the panel')
    await p2.screenshot({ path: OUT + 'wall.png' })
    await p2.goto(`${BASE}/local/overview`)
    await p2.waitForFunction(() => location.pathname === '/wall', { timeout: 10000 })

    // the big screen
    await page.goto(`${BASE}/screen`)
    await waitText('泊位', 15000)
    await waitText('可用性')
    await page.screenshot({ path: OUT + 'screen.png' })

    // a share link with a password and one download
    onHost('echo share-content-e2e > /root/e2e-share.txt')
    const sh1 = await change('file.share', { path: '/root/e2e-share.txt', hours: 1, max_downloads: 1, password: 'pw-e2e-share' })
    assert(sh1.status === 'done' && sh1.result.url.includes('/s/'), 'share made: ' + JSON.stringify(sh1.result))
    const sid = sh1.result.url.split('/s/')[1]
    const p3 = await (await browser.createBrowserContext()).newPage()
    await p3.goto(`${BASE}/s/${sid}`)
    await p3.waitForFunction(() => document.body.innerText.includes('e2e-share.txt') && document.body.innerText.includes('还能下载 1 次'), { timeout: 10000 })
    await p3.goto(`${BASE}/login`) // the share page's own CSP allows no scripts' fetches
    const tryPw = (p) => p3.evaluate(async (id, p) => { const r = await fetch('/s/' + id, { method: 'POST', body: new URLSearchParams({ password: p }) }); return { s: r.status, t: await r.text() } }, sid, p)
    assert((await tryPw('wrong')).s === 403, 'wrong password refused')
    const ok = await tryPw('pw-e2e-share')
    assert(ok.s === 200 && ok.t.trim() === 'share-content-e2e', 'downloaded: ' + ok.s)
    assert((await tryPw('pw-e2e-share')).s === 404, 'limit reached')
    await p3.browserContext().close()
    const sh2 = await change('file.share', { path: '/root/e2e-share.txt', hours: 1 })
    assert((await change('file.unshare', { id: sh2.result.url.split('/s/')[1] })).status === 'done', 'revoke')
    const gone = await page.evaluate(async (u) => (await fetch(u)).status, sh2.result.url.replace(/^https?:\/\/[^/]+/, ''))
    assert(gone === 404, 'revoked link gone: ' + gone)
  } finally {
    await ctx2.close()
    onHost('rm -f /root/e2e-share.txt')
    await change('site.delete', { id: site.result.id })
    const users = (await api('/api/v1/users')).body.items.filter((u) => u.username === 'e2efamily')
    for (const u of users) await change('user.update', { id: u.id, disabled: true })
  }
})

await spec('import', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  await page.goto(`${BASE}/local/sites`)
  await click(btn('导入…'))
  const ta = await page.waitForSelector('::-p-xpath(//*[@role="dialog"]//textarea)', { visible: true })
  await ta.type('imp-e2e.berth.test {\n  tls internal\n  reverse_proxy 127.0.0.1:18080\n}\n:9999 {\n  respond hi\n}\n')
  await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="读取"])')
  await page.waitForFunction(() => document.querySelector('[role=dialog]')?.innerText.includes('没有域名'), { timeout: 10000 })
  await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="导入选中的 1 项"])')
  await page.waitForFunction(() => document.querySelector('[role=dialog]')?.innerText.includes('完成'), { timeout: 60000 })
  const sites = (await api('/api/v1/hosts/local/sites')).body.items.filter((x) => x.domains.includes('imp-e2e.berth.test'))
  assert(sites.length === 1 && sites[0].target.upstreams[0] === '127.0.0.1:18080', 'site imported: ' + JSON.stringify(sites))
  await page.keyboard.press('Escape')
  await change('site.delete', { id: sites[0].id })
  // Nginx Proxy Manager's database read from the host
  onHost(`rm -f /tmp/npm-e2e.sqlite; sqlite3 /tmp/npm-e2e.sqlite "CREATE TABLE proxy_host (id INTEGER, domain_names TEXT, forward_scheme TEXT, forward_host TEXT, forward_port INTEGER, enabled INTEGER, ssl_forced INTEGER, access_list_id INTEGER, is_deleted INTEGER); INSERT INTO proxy_host VALUES (1, '[\\"npm-e2e.berth.test\\"]', 'http', '127.0.0.1', 18080, 1, 0, 0, 0);"`)
  const scan = await post('/api/v1/hosts/local/import/scan', { source: 'npm', path: '/tmp/npm-e2e.sqlite' })
  onHost('rm -f /tmp/npm-e2e.sqlite')
  assert(scan.status === 200 && scan.body.items.length === 1 && scan.body.items[0].ok && scan.body.items[0].kind === 'site.create', 'npm scan: ' + JSON.stringify(scan.body))
  const wrong = await post('/api/v1/hosts/local/import/scan', { source: '1panel', path: '/etc/hostname' })
  assert(wrong.status >= 400, 'not a database refused')
})

await spec('extension', async () => {
  sh('ssh', [HOST, 'rm -rf /srv/berth/ext-src/hello && mkdir -p /srv/berth/ext-src'])
  sh('scp', ['-q', '-r', 'extensions/hello', HOST + ':/srv/berth/ext-src/hello'])
  // granted only part of what it asks for: no sites:write
  const ins = await change('ext.install', { source: '/srv/berth/ext-src/hello', grant: ['containers:read', 'containers:write'] })
  if (ins.status !== 'done') throw new Error('install: ' + JSON.stringify(ins.result) + ' ' + (ins.log || '').slice(-500))
  try {
    assert(ins.status === 'done', 'install: ' + ins.status + ' ' + JSON.stringify(ins.result) + ins.log?.slice(-400))
    const ext = (await api('/api/v1/extensions')).body.items.find((e) => e.id === 'hello')
    assert(ext && ext.scopes.length === 2 && !ext.scopes.includes('sites:write'), 'granted scopes: ' + JSON.stringify(ext))
    let up = false
    for (let i = 0; i < 30 && !up; i++) { up = (await api('/api/v1/extensions')).body.items.find((e) => e.id === 'hello')?.running; if (!up) await sleep(1000) }
    assert(up, 'extension running')
    const { url } = (await api('/api/v1/extensions/hello/open')).body
    let check
    for (let i = 0; i < 20; i++) {
      check = await page.evaluate(async (u) => { const r = await fetch(u); return r.ok ? r.json() : null }, url + 'api/check?restart=' + CONTAINER)
      if (check) break
      await sleep(1000)
    }
    assert(check && check.read_containers === 200, 'may read containers: ' + JSON.stringify(check))
    assert(check.write_sites === 403 && check.write_sites_error === 'token.out_of_scope', 'may not touch sites: ' + JSON.stringify(check))
    assert(check.restart === 200 && check.restart_change, 'may restart a container: ' + JSON.stringify(check))
    const c = (await api(`/api/v1/changes/${check.restart_change}`)).body
    assert(c.actor.name === 'Hello extension' && c.actor.via === 'extension' && c.actor.type === 'ext', 'audit names the extension: ' + JSON.stringify(c.actor))
    // its page, inside the panel, in a sandboxed frame
    await page.goto(`${BASE}/local/ext/hello`)
    const frame = await page.waitForSelector('iframe[sandbox]', { timeout: 10000 })
    const f = await frame.contentFrame()
    await f.waitForFunction(() => /\d+/.test(document.getElementById('n')?.textContent ?? ''), { timeout: 15000 })
    // the frame cannot use the viewer's session
    const leaked = await f.evaluate(async () => { try { const r = await fetch('/api/v1/me'); return r.status } catch { return 'blocked' } })
    assert(leaked !== 200, 'the frame has no session: ' + leaked)
    const events = await page.evaluate(async (u) => (await fetch(u + 'api/events')).json(), url)
    assert(events.some((e) => e.type === 'change.done'), 'events delivered: ' + events.length)
    await page.screenshot({ path: OUT + 'extension.png' })
  } finally {
    const rm = await change('ext.remove', { id: 'hello' })
    assert(rm.status === 'done', 'removed')
    assert(!(await api('/api/v1/extensions')).body.items.some((e) => e.id === 'hello'), 'gone from the list')
    sh('ssh', [HOST, 'rm -rf /srv/berth/stacks/.removed/ext-hello-* /srv/berth/ext-src; docker image rm -f berth-ext-hello:1.0.0 >/dev/null 2>&1; true'])
  }
})

await spec('ai-assistant', async () => {
  sh('scp', ['-q', ROOT + 'test/e2e/fixtures/fake-llm.py', `${HOST}:/tmp/e2e-fake-llm.py`])
  sh('ssh', [HOST, 'pkill -f "^python3 /tmp/e2e-fake-llm" || true; nohup python3 /tmp/e2e-fake-llm.py 18765 >/dev/null 2>&1 & sleep 0.5'])
  try {
    const set = await change('ai.set', { provider: 'openai', base_url: 'http://127.0.0.1:18765/v1', model: 'fake', key: 'sk-e2e-not-real', monthly_limit: 100000 })
    assert(set.status === 'done', 'ai.set')
    assert(!JSON.stringify(set.params).includes('sk-e2e'), 'key redacted in the audit log')
    await page.goto(`${BASE}/local/overview`)
    await click(btn('AI 助手'))
    const ta = await page.waitForSelector('::-p-xpath(//*[@role="dialog"]//textarea)', { visible: true })
    await ta.type('帮我看看')
    await page.keyboard.press('Enter')
    await page.waitForFunction(() => document.querySelector('[role=dialog]')?.innerText.includes('建议加一个可用性检测'), { timeout: 30000 })
    await page.screenshot({ path: OUT + 'ai.png' })
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="预览并执行…"])')
    await click('[data-testid=confirm]:not([disabled])', 15000)
    for (let i = 0; i < 20; i++) { if ((await api('/api/v1/uptime')).body.items.some((c) => c.name === 'e2e-ai')) break; await sleep(500) }
    const checks = (await api('/api/v1/uptime')).body.items.filter((c) => c.name === 'e2e-ai')
    assert(checks.length === 1, 'the proposal ran only after confirming')
    for (const c of checks) await change('uptime.delete', { id: c.id })
    const st = (await api('/api/v1/ai')).body
    assert(st.month >= 300, 'usage counted: ' + st.month)
  } finally {
    await change('ai.set', { provider: '' })
    sh('ssh', [HOST, 'pkill -f "^python3 /tmp/e2e-fake-llm"; rm -f /tmp/e2e-fake-llm.py; true'])
  }
})

await spec('multi-host', async () => {
  const onH2 = (cmd) => sh('ssh', [HOST, `incus exec h2 -- sh -c ${JSON.stringify(cmd)}`])
  // the VM is kept stopped between runs: start it for the test, stop it again afterwards
  const wasStopped = sh('ssh', [HOST, 'incus list h2 -c s -f csv']) !== 'RUNNING'
  if (wasStopped) {
    sh('ssh', [HOST, 'incus start h2'])
    for (let i = 0; i < 60; i++) { try { onH2('systemctl is-system-running --wait >/dev/null 2>&1; true'); break } catch { await sleep(2000) } }
  }
  // a clean start on the VM, whatever a previous run left
  onH2('systemctl disable --now berth-agent 2>/dev/null; rm -rf /etc/berth /var/lib/berth /usr/local/bin/berth; true')
  const jc = await post('/api/v1/hosts/join-codes', { name: 'h2' })
  assert(jc.status === 200, 'join code: ' + JSON.stringify(jc.body))
  const cmd = jc.body.command.replaceAll('https://localhost:7092', 'https://10.42.231.1:7092')
  onH2(cmd)
  let h2
  for (let i = 0; i < 40; i++) { h2 = (await api('/api/v1/hosts')).body.items.find((h) => h.name === 'h2'); if (h2?.online) break; await sleep(1000) }
  assert(h2?.online, 'h2 joined and online: ' + JSON.stringify(h2))
  try {
    // every area answers for the second host
    for (const p of ['overview', 'containers', 'system/info', 'system/services', 'system/storage', 'system/tuning', 'system/ssh', 'system/accounts', 'jobs', 'drift', 'firewall', 'files/list?path=/root']) {
      const r = await api(`/api/v1/hosts/h2/${p}`)
      assert(r.status === 200, `h2 ${p}: ${r.status} ${JSON.stringify(r.body).slice(0, 200)}`)
    }
    // a terminal on it
    const term = await post('/api/v1/hosts/h2/terminals', { target: 'host', cols: 80, rows: 24 })
    assert(term.status === 200 && term.body.id, 'terminal on h2: ' + JSON.stringify(term.body))
    await page.evaluate(async (id) => fetch(`/api/v1/hosts/h2/terminals/${id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': (await fetch('/api/v1/me').then((r) => r.json())).csrf } }), term.body.id)
    // a scheduled job runs there
    const job = await post('/api/v1/hosts/h2/changes/cron.set', { name: 'h2 job', cron: '@daily', action: { type: 'script', script: 'hostname' } })
    assert(job.status === 200, 'job on h2')
    await post('/api/v1/hosts/h2/changes/cron.run', { id: job.body.result.id })
    let runs
    for (let i = 0; i < 20; i++) { runs = (await api(`/api/v1/hosts/h2/jobs/${job.body.result.id}/runs`)).body.items; if (runs[0]?.status === 'ok') break; await sleep(500) }
    assert(runs[0]?.status === 'ok' && runs[0].output.trim() === 'h2', 'job ran on h2: ' + JSON.stringify(runs[0]))
    await post('/api/v1/hosts/h2/changes/cron.delete', { id: job.body.result.id })
    // an uptime check seen from h2
    const chk = await change('uptime.set', { name: 'from h2', kind: 'tcp', target: '10.42.231.1:7092', interval: 30, from: 'h2' })
    let rec = []
    for (let i = 0; i < 50 && rec.length === 0; i++) { rec = (await api(`/api/v1/uptime/${chk.result.id}/recent`)).body.items; if (!rec.length) await sleep(1000) }
    assert(rec.length && rec[0].ok, 'checked from h2: ' + JSON.stringify(rec))
    await change('uptime.delete', { id: chk.result.id })
    // the UI on the second host
    await page.goto(`${BASE}/h2/system`)
    await waitText('Debian', 15000)
    await page.goto(`${BASE}/h2/overview`)
    await waitText('这台服务器', 15000)
    await page.goto(`${BASE}/local/hosts`)
    await waitText('h2', 10000)
    await page.screenshot({ path: OUT + 'hosts.png' })
    // one-click agent upgrade: give h2 an "older" agent, then upgrade it from the hosts page
    sh('go', ['build', '-trimpath', '-ldflags', '-s -w -X github.com/wangsirco/berth/internal/version.Version=0.0.1-e2e-old', '-o', OUT + 'berth-old', './cmd/berth'], { env: { ...process.env, PATH: '/opt/homebrew/bin:' + process.env.PATH, GOOS: 'linux', GOARCH: 'arm64', CGO_ENABLED: '0' } })
    sh('scp', ['-q', OUT + 'berth-old', HOST + ':/tmp/berth-old'])
    sh('ssh', [HOST, 'incus file push /tmp/berth-old h2/usr/local/bin/berth.old && rm /tmp/berth-old'])
    onH2('chmod 755 /usr/local/bin/berth.old && mv /usr/local/bin/berth.old /usr/local/bin/berth && systemctl restart berth-agent')
    for (let i = 0; i < 40; i++) { h2 = (await api('/api/v1/hosts')).body.items.find((h) => h.name === 'h2'); if (h2?.online && h2.version === '0.0.1-e2e-old') break; await sleep(1000) }
    assert(h2.version === '0.0.1-e2e-old', 'h2 runs the old agent: ' + JSON.stringify(h2))
    await page.reload()
    await click(btn('升级 agent'), 15000)
    await waitText('0.0.1-e2e-old', 15000)
    await click('[data-testid=confirm]:not([disabled])', 15000)
    const panelVer = h2.upgrade
    assert(panelVer && panelVer !== '0.0.1-e2e-old', 'an upgrade is offered: ' + JSON.stringify(h2))
    for (let i = 0; i < 120; i++) { h2 = (await api('/api/v1/hosts')).body.items.find((h) => h.name === 'h2'); if (h2?.online && h2.version === panelVer) break; await sleep(1000) }
    assert(h2.version === panelVer, 'h2 upgraded to ' + panelVer + ': ' + JSON.stringify(h2))
    let done
    for (let i = 0; i < 30; i++) { done = (await api('/api/v1/changes?kind=host.agent_update')).body.items[0]; if (done && done.status !== 'running') break; await sleep(1000) }
    assert(done?.status === 'done', 'upgrade change done: ' + JSON.stringify(done).slice(0, 300))
    assert(onH2('ls /var/lib/berth/rollback/').includes('agent-'), 'old binary kept for rollback')
    assert((await api('/api/v1/hosts/h2/system/info')).status === 200, 'h2 answers after the upgrade')
  } finally {
    await change('host.remove', { name: 'h2' })
    onH2('systemctl disable --now berth-agent 2>/dev/null; true')
    if (wasStopped) sh('ssh', [HOST, 'incus stop h2'])
  }
})

await spec('file-sync', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const onH2 = (cmd) => sh('ssh', [HOST, `incus exec h2 -- sh -c ${JSON.stringify(cmd)}`])
  const wasStopped = onHost('incus list h2 -c s -f csv') !== 'RUNNING'
  if (wasStopped) {
    onHost('incus start h2')
    for (let i = 0; i < 60; i++) { try { onH2('systemctl is-system-running --wait >/dev/null 2>&1; true'); break } catch { await sleep(2000) } }
  }
  onH2('systemctl disable --now berth-agent 2>/dev/null; rm -rf /etc/berth /var/lib/berth /usr/local/bin/berth /srv/e2e-sync-dst; sed -i "/berth-sync-/d" /root/.ssh/authorized_keys 2>/dev/null; true')
  onHost('rm -rf /srv/e2e-sync && mkdir -p /srv/e2e-sync/sub && echo hello-sync > /srv/e2e-sync/a.txt && echo deep > /srv/e2e-sync/sub/b.txt')
  const jc = await post('/api/v1/hosts/join-codes', { name: 'h2' })
  onH2(jc.body.command.replaceAll('https://localhost:7092', 'https://10.42.231.1:7092'))
  let h2
  for (let i = 0; i < 40; i++) { h2 = (await api('/api/v1/hosts')).body.items.find((h) => h.name === 'h2'); if (h2?.online) break; await sleep(1000) }
  assert(h2?.online, 'h2 online')
  try {
    await page.goto(`${BASE}/local/files/srv/e2e-sync`)
    await waitText('a.txt')
    await click(btn('同步'))
    await click(btn('新建同步'))
    await fill('对方的目录', '/srv/e2e-sync-dst')
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="下一步"][not(@disabled)])')
    await waitText('并马上同步一次', 15000)
    await click('[data-testid=confirm]:not([disabled])', 15000)
    let c
    for (let i = 0; i < 120; i++) { c = (await api('/api/v1/changes?kind=sync.set')).body.items[0]; if (c && !['running', 'queued', 'planned'].includes(c.status)) break; await sleep(1000) }
    assert(c?.status === 'done', 'sync set up: ' + JSON.stringify(c).slice(0, 600))
    assert(onH2('cat /srv/e2e-sync-dst/a.txt /srv/e2e-sync-dst/sub/b.txt') === 'hello-sync\ndeep', 'files arrived on h2')
    // the key can do nothing else
    const key = '/var/lib/berth/sync/id_ed25519'
    const sshOpts = `-i ${key} -o BatchMode=yes -o UserKnownHostsFile=/var/lib/berth/sync/known_hosts`
    assert(onHost(`ssh ${sshOpts} root@10.42.231.42 id 2>&1; true`).includes('only rsync is allowed'), 'no shell with the sync key')
    assert(onHost(`rsync -a -e 'ssh ${sshOpts}' root@10.42.231.42:/etc/hostname /tmp/e2e-steal 2>&1; true`).includes('may only send'), 'cannot read with the sync key')
    assert(onHost(`rsync -a -e 'ssh ${sshOpts}' /srv/e2e-sync/a.txt root@10.42.231.42:../../etc/e2e-x 2>&1; true`).includes('outside'), 'cannot escape the folder')
    // a change on the source, then a run from the drawer
    onHost('echo changed > /srv/e2e-sync/a.txt')
    await page.reload()
    await click(btn('同步'))
    await waitText('/srv/e2e-sync → h2:/srv/e2e-sync-dst', 10000)
    const list = (await api('/api/v1/hosts/local/syncs')).body.items
    assert(list.length === 1 && list[0].last_run?.status === 'ok', 'listed with its last run: ' + JSON.stringify(list))
    await change('cron.run', { id: list[0].job_id })
    for (let i = 0; i < 30 && onH2('cat /srv/e2e-sync-dst/a.txt') !== 'changed'; i++) await sleep(1000)
    assert(onH2('cat /srv/e2e-sync-dst/a.txt') === 'changed', 'second run synced the change')
    await page.screenshot({ path: OUT + 'file-sync.png' })
    // deleting the sync takes the key away on h2; the copied files stay
    assert((await change('sync.delete', { job: list[0].job_id })).status === 'done', 'sync deleted')
    assert(!onH2('cat /root/.ssh/authorized_keys').includes('berth-sync-'), 'key removed on h2')
    assert(onH2('cat /srv/e2e-sync-dst/sub/b.txt') === 'deep', 'copied files kept')
  } finally {
    for (const s of (await api('/api/v1/hosts/local/syncs')).body.items ?? []) await change('sync.delete', { job: s.job_id })
    await change('host.remove', { name: 'h2' })
    onH2('systemctl disable --now berth-agent 2>/dev/null; rm -rf /srv/e2e-sync-dst; sed -i "/berth-sync-/d" /root/.ssh/authorized_keys 2>/dev/null; true')
    onHost('rm -rf /srv/e2e-sync /tmp/e2e-steal')
    if (wasStopped) onHost('incus stop h2')
  }
})

await spec('php-native', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd], { timeout: 900000 })
  const waitC = async (c) => { for (let i = 0; i < 1200 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const D = 'phpn-e2e.berth.test', O = 'phpn-other.berth.test'
  const viaSite = (path) => onHost(`curl -sk --max-time 10 --resolve ${D}:443:127.0.0.1 https://${D}${path} || true`)
  const inst = await waitC(await change('php.native.install', { version: '8.4' }))
  assert(inst.status === 'done', 'PHP 8.4 installed natively: ' + JSON.stringify(inst.result ?? inst.error ?? inst.log?.slice(-600)))
  const rts = (await api('/api/v1/hosts/local/php/native')).body.items
  assert(rts.find((r) => r.version === '8.4')?.installed && rts.find((r) => r.version === '8.4').extensions.includes('pdo_mysql'), 'listed with extensions: ' + JSON.stringify(rts).slice(0, 300))
  let site
  try {
    onHost(`rm -rf /srv/berth/caddy/sites/${D} /srv/berth/caddy/sites/${O}; mkdir -p /srv/berth/caddy/sites/${O}/public && echo secret > /srv/berth/caddy/sites/${O}/keep.txt`)
    site = await waitC(await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'php', php: '84', native: true, root_sub: 'public',
      php_workers: [{ name: 'tick', command: `php -r 'while (true) { file_put_contents("worker.txt", time()); sleep(2); }'` }], schedule: `php -r 'file_put_contents("sched.txt", time());'` } }))
    assert(site.status === 'done', 'native PHP site: ' + JSON.stringify(site.result ?? site.error ?? site.log?.slice(-600)))
    const id = site.result.id
    onHost(`mkdir -p /srv/berth/caddy/sites/${D}/public && cat > /srv/berth/caddy/sites/${D}/public/index.php <<'X'
<?php
$who = posix_getpwuid(posix_geteuid())['name'];
$w = @file_put_contents(__DIR__ . '/../written.txt', 'x') ? 'ok' : 'denied';
$o = @file_get_contents('/srv/berth/caddy/sites/${O}/keep.txt') === false ? 'denied' : 'read';
$e = @file_get_contents('/etc/shadow') === false ? 'denied' : 'read';
echo "$who|" . ini_get('memory_limit') . "|$w|$o|$e|" . PHP_VERSION;
X
chown -R site-web${id}: /srv/berth/caddy/sites/${D}`)
    let body = ''
    for (let i = 0; i < 20 && !body.includes('|'); i++) { body = viaSite('/'); if (!body.includes('|')) await sleep(1000) }
    const [who, mem, w, o, e, ver] = body.split('|')
    assert(who === 'site-web' + id, 'runs as the site user: ' + body)
    assert(mem === '256M' && w === 'ok' && o === 'denied' && e === 'denied' && ver.startsWith('8.4'), 'sandbox: ' + body)
    // php.ini per site
    const s1 = (await api(`/api/v1/hosts/local/sites/${id}`)).body.site
    const up = await waitC(await change('site.update', { ...s1, target: { ...s1.target, php_ini: { memory_limit: '384M' } } }))
    assert(up.status === 'done', 'ini update: ' + JSON.stringify(up.error ?? up.body ?? up).slice(0, 400))
    for (let i = 0; i < 10 && !viaSite('/').includes('384M'); i++) await sleep(1000)
    assert(viaSite('/').includes('|384M|'), 'memory_limit now 384M: ' + viaSite('/'))
    // the worker runs, the scheduler fires every minute
    let st
    for (let i = 0; i < 90; i++) { st = (await api(`/api/v1/hosts/local/sites/${id}/php`)).body.status; if (st?.workers?.tick === 'active' && onHost(`test -s /srv/berth/caddy/sites/${D}/sched.txt && echo y || echo n`) === 'y') break; await sleep(2000) }
    assert(st?.fpm === 'active' && st.workers.tick === 'active', 'FPM and worker active: ' + JSON.stringify(st))
    assert(onHost(`test -s /srv/berth/caddy/sites/${D}/worker.txt && echo y || echo n`) === 'y', 'the worker writes')
    assert(onHost(`test -s /srv/berth/caddy/sites/${D}/sched.txt && echo y || echo n`) === 'y', 'the scheduler ran')
    assert(onHost(`stat -c %U /srv/berth/caddy/sites/${D}/sched.txt`) === 'site-web' + id, 'as the site user')
    // a command in the site's folder
    const run = await waitC(await change('site.php_run', { id, command: 'composer --version > composer.txt && php -v > phpv.txt' }))
    assert(run.status === 'done', 'site.php_run: ' + JSON.stringify(run.error ?? ''))
    const outs = onHost(`cat /srv/berth/caddy/sites/${D}/composer.txt /srv/berth/caddy/sites/${D}/phpv.txt; stat -c %U /srv/berth/caddy/sites/${D}/composer.txt`)
    assert(/Composer version/.test(outs) && /PHP 8\.4/.test(outs) && outs.trim().endsWith('site-web' + id), 'composer and php ran in the site as its user: ' + outs.slice(0, 300))
    assert(onHost('nft list table inet berth_proj').includes('20000-29999'), 'the FPM port is not reachable from outside')
    await page.goto(`${BASE}/local/sites/php`)
    await waitText('原生 PHP', 10000)
    await waitText(D, 10000) // listed as using 8.4
    await page.goto(`${BASE}/local/sites/s/${id}`)
    await click('::-p-aria([name="PHP"][role="radio"])')
    await waitText('这个网站的 PHP 设置', 10000)
    await waitText('site-web' + id)
    await page.screenshot({ path: OUT + 'php-native.png' })
  } finally {
    if (site?.result?.id) await change('site.delete', { id: site.result.id })
    onHost(`rm -rf /srv/berth/caddy/sites/${D} /srv/berth/caddy/sites/${O}; true`)
  }
  const gone = onHost(`ls /etc/systemd/system | grep -c '^berth-php-' || true; getent passwd | grep -c '^site-web' || true`)
  assert(gone === '0\n0', 'units and user removed with the site: ' + gone)
})

await spec('php-wordpress', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitC = async (c) => { for (let i = 0; i < 1200 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const ver = '8.4'
  const php = await change('php.set', { version: ver })
  // the first build compiles extensions: allow it time
  let c = php
  for (let i = 0; i < 900 && ['running', 'queued'].includes(c.status); i++) { await sleep(2000); c = (await api(`/api/v1/changes/${php.id}`)).body }
  assert(c.status === 'done', 'php built: ' + c.status + ' ' + JSON.stringify(c.result).slice(0, 300))
  onHost('rm -rf /srv/berth/apps/wpdb') // data from an earlier run would keep its old root password
  const db = await change('app.install', { app: 'mariadb', name: 'wpdb' })
  let site, inst, dz, mig, nwp
  try {
    let dc = db
    for (let i = 0; i < 300 && ['running', 'queued'].includes(dc.status); i++) { await sleep(2000); dc = (await api(`/api/v1/changes/${db.id}`)).body }
    assert(dc.status === 'done', 'mariadb installed')
    const cont = (await api('/api/v1/hosts/local/databases')).body.candidates.find((x) => x.container.startsWith('wpdb'))
    for (let i = 0; i < 30; i++) { // the database accepts connections a little after it starts
      inst = await change('db.instance.add', { name: 'wpdb', engine: 'mariadb', container: cont.container })
      if (inst.status === 'done') break
      await sleep(2000)
    }
    assert(inst?.status === 'done', 'db instance: ' + JSON.stringify(inst?.result ?? inst))
    // the dialog's way: a PHP site, then WordPress into it
    site = await change('site.create', { domains: ['wp-e2e.berth.test'], tls: { mode: 'internal' }, target: { type: 'php', php: '84' } })
    assert(site.status === 'done', 'php site: ' + JSON.stringify(site.result))
    let cms = await change('site.cms', { id: site.result.id, app: 'wordpress', db: inst.result.id })
    for (let i = 0; i < 120 && ['running', 'queued'].includes(cms.status); i++) { await sleep(2000); cms = (await api(`/api/v1/changes/${cms.id}`)).body }
    assert(cms.status === 'done', 'wordpress: ' + JSON.stringify(cms.result).slice(0, 400))
    // through Caddy: WordPress answers and sends a new install to its setup page
    let page1 = ''
    for (let i = 0; i < 15; i++) {
      page1 = onHost('curl -sk -o /dev/null -w "%{http_code} %{redirect_url}" --resolve wp-e2e.berth.test:443:127.0.0.1 https://wp-e2e.berth.test/ || true')
      if (page1.startsWith('302')) break
      await sleep(2000)
    }
    assert(page1.startsWith('302') && page1.includes('wp-admin/install.php'), 'wordpress redirects to its installer: ' + page1)
    const inst2 = onHost('curl -sk --resolve wp-e2e.berth.test:443:127.0.0.1 https://wp-e2e.berth.test/wp-admin/install.php | head -c 20000')
    assert(/WordPress/.test(inst2) && !/Error establishing a database connection/.test(inst2), 'installer page reaches the database')
    assert(onHost('docker exec berth-php84 stat -c %U /srv/sites/wp-e2e.berth.test/wp-config.php') === 'www-data', 'owned by the PHP user')
    const fix = await change('site.fix_perms', { id: site.result.id })
    assert(fix.status === 'done', 'fix perms')
    // a container PHP site moves to its own native FPM, and back
    const nat = await waitC(await change('php.native.install', { version: '8.4' }))
    assert(nat.status === 'done', 'native 8.4')
    mig = await change('site.create', { domains: ['mig-e2e.berth.test'], tls: { mode: 'internal' }, target: { type: 'php', php: '84' } })
    assert(mig.status === 'done', 'container php site')
    onHost(`mkdir -p /srv/berth/caddy/sites/mig-e2e.berth.test && echo '<?php echo posix_getpwuid(posix_geteuid())["name"];' > /srv/berth/caddy/sites/mig-e2e.berth.test/index.php && docker exec berth-php84 chown -R www-data: /srv/sites/mig-e2e.berth.test`)
    const who = () => onHost('curl -sk --max-time 10 --resolve mig-e2e.berth.test:443:127.0.0.1 https://mig-e2e.berth.test/ || true')
    for (let i = 0; i < 15 && who() !== 'www-data'; i++) await sleep(1000)
    assert(who() === 'www-data', 'served by the shared container: ' + who())
    const ms = (await api(`/api/v1/hosts/local/sites/${mig.result.id}`)).body.site
    const toNative = await waitC(await change('site.update', { ...ms, target: { ...ms.target, native: true } }))
    assert(toNative.status === 'done', 'moved to native: ' + JSON.stringify(toNative.error ?? ''))
    for (let i = 0; i < 15 && who() !== 'site-web' + mig.result.id; i++) await sleep(1000)
    assert(who() === 'site-web' + mig.result.id, 'served by its own FPM: ' + who())
    const back = await waitC((await post(`/api/v1/changes/${toNative.id}/revert`, {})).body)
    assert(back.status === 'done', 'moved back')
    for (let i = 0; i < 15 && who() !== 'www-data'; i++) await sleep(1000)
    assert(who() === 'www-data' && !/site-web/.test(onHost('stat -c %U /srv/berth/caddy/sites/mig-e2e.berth.test/index.php')), 'back in the container, files given back')
    // WordPress into a native site: the database is reached where its container publishes it
    nwp = await change('site.create', { domains: ['nwp-e2e.berth.test'], tls: { mode: 'internal' }, target: { type: 'php', php: '84', native: true } })
    assert(nwp.status === 'done', 'native site for WordPress: ' + JSON.stringify(nwp.result ?? nwp.error))
    let ncms = await change('site.cms', { id: nwp.result.id, app: 'wordpress', db: inst.result.id })
    for (let i = 0; i < 150 && ['running', 'queued'].includes(ncms.status); i++) { await sleep(2000); ncms = (await api(`/api/v1/changes/${ncms.id}`)).body }
    assert(ncms.status === 'done' && /^127\.0\.0\.1:\d+$/.test(ncms.result.db.host), 'native WordPress uses the published port: ' + JSON.stringify(ncms.result ?? ncms.error))
    let ninst = ''
    for (let i = 0; i < 15; i++) { ninst = onHost('curl -sk --resolve nwp-e2e.berth.test:443:127.0.0.1 https://nwp-e2e.berth.test/wp-admin/install.php | head -c 20000 || true'); if (/WordPress/.test(ninst)) break; await sleep(2000) }
    assert(/WordPress/.test(ninst) && !/Error establishing a database connection/.test(ninst), 'native WordPress reaches its database')
    // Discuz! into a second site: its installer opens and finds everything it needs
    dz = await change('site.create', { domains: ['dz-e2e.berth.test'], tls: { mode: 'internal' }, target: { type: 'php', php: '84' } })
    assert(dz.status === 'done', 'discuz site')
    let dzc = await change('site.cms', { id: dz.result.id, app: 'discuz', db: inst.result.id })
    for (let i = 0; i < 150 && ['running', 'queued'].includes(dzc.status); i++) { await sleep(2000); dzc = (await api(`/api/v1/changes/${dzc.id}`)).body }
    assert(dzc.status === 'done' && dzc.result.next === 'discuz' && dzc.result.db?.name, 'discuz: ' + JSON.stringify(dzc.result ?? dzc.log).slice(0, 400))
    let env1 = ''
    for (let i = 0; i < 15; i++) {
      env1 = onHost('curl -sk --resolve dz-e2e.berth.test:443:127.0.0.1 "https://dz-e2e.berth.test/install/index.php?step=1" | head -c 60000 || true')
      if (env1.includes('Discuz')) break
      await sleep(2000)
    }
    assert(env1.includes('Discuz') && !/不可写|不支持|不存在/.test(env1), 'discuz installer environment check passes: ' + env1.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').slice(0, 600))
    await page.goto(`${BASE}/local/sites/php`)
    await waitText('PHP 8.4', 10000)
    await waitText('wp-e2e.berth.test')
    await page.screenshot({ path: OUT + 'php.png' })
  } finally {
    if (site?.result?.id) await change('site.delete', { id: site.result.id })
    if (dz?.result?.id) await change('site.delete', { id: dz.result.id })
    if (mig?.result?.id) await change('site.delete', { id: mig.result.id })
    if (nwp?.result?.id) await change('site.delete', { id: nwp.result.id })
    onHost('rm -rf /srv/berth/caddy/sites/mig-e2e.berth.test /srv/berth/caddy/sites/nwp-e2e.berth.test; true')
    await change('php.native.remove', { version: '8.4' })
    if (inst?.result?.id) await change('db.instance.remove', { instance: inst.result.id })
    await change('app.remove', { name: 'wpdb' })
    onHost('rm -rf /srv/berth/caddy/sites/dz-e2e.berth.test /srv/berth/caddy/sites/wp-e2e.berth.test /srv/berth/apps/wpdb; docker volume ls -q | grep -i wpdb | xargs -r docker volume rm >/dev/null 2>&1; true')
    const rm = await change('php.remove', { version: ver })
    for (let i = 0; i < 60 && ['running', 'queued'].includes(rm.status); i++) await sleep(1000)
  }
})

await spec('projects', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  onHost(`rm -rf /srv/berth/e2e-proj && mkdir -p /srv/berth/e2e-proj && printf '%s' "require('http').createServer((q, s) => s.end('hello-proj ' + process.version)).listen(process.env.PORT)" > /srv/berth/e2e-proj/server.js && (id e2eproj >/dev/null 2>&1 || useradd --system --no-create-home --shell /sbin/nologin e2eproj) && chown -R e2eproj /srv/berth/e2e-proj`)
  let c = await change('project.set', { name: 'e2e-node', kind: 'node', version: '22', dir: '/srv/berth/e2e-proj', command: 'node server.js', user: 'e2eproj', port: 18911 })
  for (let i = 0; i < 300 && ['running', 'queued'].includes(c.status); i++) { await sleep(2000); c = (await api(`/api/v1/changes/${c.id}`)).body }
  try {
    assert(c.status === 'done', 'project: ' + c.status + ' ' + (c.log || '').slice(-400))
    let body = ''
    for (let i = 0; i < 20 && !body.startsWith('hello-proj'); i++) { body = onHost('curl -s http://127.0.0.1:18911/ || true'); if (!body.startsWith('hello-proj')) await sleep(1000) }
    assert(/^hello-proj v22\./.test(body), 'node 22 serves: ' + body)
    // it comes back after a crash
    onHost('pkill -u e2eproj -f "node server.js"; true')
    body = ''
    for (let i = 0; i < 20 && !body.startsWith('hello-proj'); i++) { await sleep(1000); body = onHost('curl -s http://127.0.0.1:18911/ || true') }
    assert(body.startsWith('hello-proj'), 'restarted after a crash')
    await page.goto(`${BASE}/local/projects`)
    await waitText('e2e-node', 10000)
    await waitText('建站点')
    await page.screenshot({ path: OUT + 'projects.png' })
  } finally {
    await change('project.delete', { name: 'e2e-node' })
    assert(onHost('systemctl is-active berth-app-e2e-node.service || true') !== 'active', 'stopped')
    onHost('rm -rf /srv/berth/e2e-proj; userdel e2eproj 2>/dev/null; true')
  }
})

await spec('container-upkeep', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitC = async (c) => { for (let i = 0; i < 150 && ['running', 'queued'].includes(c.status); i++) { await sleep(2000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  // limits written into compose and applied to the container
  const lim = await waitC(await change('compose.limits', { name: PROJECT, service: 'web', cpus: '0.5', memory: '64m' }))
  assert(lim.status === 'done', 'limits: ' + JSON.stringify(lim.result))
  assert(onHost(`docker inspect -f '{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}}' ${CONTAINER}`) === '500000000 67108864', 'limits applied')
  const un = await post(`/api/v1/changes/${lim.id}/revert`, {})
  const unc = await waitC(un.body)
  assert(unc.status === 'done' && onHost(`docker inspect -f '{{.HostConfig.NanoCpus}} {{.HostConfig.Memory}}' ${CONTAINER}`) === '0 0', 'limits undone')
  // updates are checked against the registry
  const upd = (await api('/api/v1/hosts/local/image-updates?refresh=1')).body
  const web = upd.items.find((u) => u.container === CONTAINER)
  assert(web && !web.error, 'update check for the smoke container: ' + JSON.stringify(web))
  const up = await waitC(await change('compose.upgrade', { name: PROJECT }))
  assert(up.status === 'done', 'upgrade: ' + JSON.stringify(up.result) + (up.log || '').slice(-300))
  assert(onHost(`docker inspect -f '{{.State.Running}}' ${CONTAINER}`) === 'true', 'running after the upgrade')
  // a mirror goes into daemon.json without restarting containers, and comes out again
  const started = onHost(`docker inspect -f '{{.State.StartedAt}}' ${CONTAINER}`)
  const m = await waitC(await change('docker.mirrors', { mirrors: ['https://mirror.gcr.io'] }))
  assert(m.status === 'done' && onHost('cat /etc/docker/daemon.json').includes('mirror.gcr.io'), 'mirror set')
  const mu = await waitC((await post(`/api/v1/changes/${m.id}/revert`, {})).body)
  assert(mu.status === 'done' && !onHost('cat /etc/docker/daemon.json').includes('mirror.gcr.io'), 'mirror removed')
  assert(onHost(`docker inspect -f '{{.State.StartedAt}}' ${CONTAINER}`) === started, 'containers kept running')
  await page.goto(`${BASE}/local/containers/updates`)
  await waitText('镜像更新', 10000)
  await waitText(PROJECT, 150000) // registry lookups for every running image; slower while many containers run
  await page.screenshot({ path: OUT + 'updates.png' })
})

await spec('site-ui', async () => {
  // domains as tags, a note shown in place of them, and the pages that left Sites and Network
  const D = 'ui-e2e.berth.test', W = 'www.ui-e2e.berth.test'
  let id = 0
  try {
    await page.goto(`${BASE}/local/sites`)
    await click(btn('＋ 新建站点'))
    const box = await page.waitForSelector('::-p-aria([name="域名"][role="textbox"])', { visible: true })
    await box.type(D); await page.keyboard.press('Enter')
    await box.type(W + ',')
    await box.type('tmp.ui-e2e.berth.test '); await click(`::-p-aria([name="× tmp.ui-e2e.berth.test"])`)
    const tags = () => page.evaluate(() => [...document.querySelectorAll('[role=dialog] button[aria-label^="× "]')].map((b) => b.getAttribute('aria-label').slice(2)))
    assert(JSON.stringify(await tags()) === JSON.stringify([D, W]), 'two tags: ' + JSON.stringify(await tags()))
    await click(`::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="${W}"])`) // make it the primary
    assert((await tags())[0] === W, 'www is primary now')
    await click(`::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="${D}"])`)
    await fill('备注', 'E2E 官网')
    await click(text('静态目录'))
    await click(text('内部 CA'))
    await click(btn('创建站点'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    await page.waitForFunction(() => document.querySelectorAll('[role=dialog]').length === 0, { timeout: 20000 })
    await waitText('E2E 官网', 15000)
    await waitText('+1 个域名')
    const s = (await api('/api/v1/hosts/local/sites')).body.items.find((x) => x.domains.includes(D))
    id = s?.id ?? 0
    assert(s && s.domains[0] === D && s.domains[1] === W && s.options.note === 'E2E 官网', 'saved: ' + JSON.stringify(s))
    await page.screenshot({ path: OUT + 'sites-note.png' })
    // projects and domains have their own pages; the old addresses lead there
    await waitText('Cloudflare')
    await page.goto(`${BASE}/local/sites/projects`)
    await page.waitForFunction(() => location.pathname === '/local/projects', { timeout: 10000 })
    await waitText('全部项目', 10000)
    await page.goto(`${BASE}/local/network?tab=cloudflare`)
    await page.waitForFunction(() => location.pathname === '/local/cloudflare', { timeout: 10000 })
    await page.goto(`${BASE}/local/sites/dns`)
    await page.waitForFunction(() => location.pathname === '/local/domains', { timeout: 10000 })
    await waitText('DNS 账号', 10000)
    await page.screenshot({ path: OUT + 'domains.png' })
  } finally {
    if (id) await change('site.delete', { id })
  }
})

await spec('site-protect', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const D = 'prot-e2e.berth.test'
  const curl = (path, ref) => onHost(`curl -sk -o /dev/null -w "%{http_code}" --resolve ${D}:443:127.0.0.1 ${ref ? `-H "Referer: ${ref}"` : ''} https://${D}${path} || true`)
  onHost(`[ -f /srv/berth/caddy/logs/${D}.log ] && : > /srv/berth/caddy/logs/${D}.log; true`) // no lines from an earlier run
  onHost(`mkdir -p /srv/berth/caddy/sites/${D}/uploads && echo hi > /srv/berth/caddy/sites/${D}/index.html && head -c 100 /dev/urandom > /srv/berth/caddy/sites/${D}/a.jpg`)
  const site = await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'static' },
    options: { hotlink: { exts: ['jpg'], allow: ['*.friend.org'], allow_empty: true } } })
  assert(site.status === 'done', 'site: ' + JSON.stringify(site.result))
  try {
    let code = ''
    for (let i = 0; i < 15 && code !== '200'; i++) { code = curl('/a.jpg'); if (code !== '200') await sleep(1000) }
    assert(code === '200', 'no referer allowed: ' + code)
    assert(curl('/a.jpg', 'https://evil.example/page') === '403', 'other site blocked')
    assert(curl('/a.jpg', `https://${D}/x`) === '200' && curl('/a.jpg', 'https://cdn.friend.org/p') === '200', 'own and allowed sites pass')
    assert(curl('/index.html', 'https://evil.example/') === '200', 'pages are not affected')
    // the access log view
    let log = []
    for (let i = 0; i < 20 && !log.some((l) => l.status === 403); i++) { log = (await api(`/api/v1/hosts/local/sites/${site.result.id}/log?lines=50`)).body.items; if (!log.some((l) => l.status === 403)) await sleep(1000) }
    assert(log.some((l) => l.status === 403 && l.uri === '/a.jpg'), 'log shows the blocked request: ' + JSON.stringify(log).slice(0, 400) + ' / ' + onHost(`ls -la /srv/berth/caddy/logs/${D}.log 2>&1; tail -c 600 /srv/berth/caddy/logs/${D}.log 2>&1; curl -s --unix-socket /srv/berth/caddy/run/admin.sock http://x/config/apps/http/servers/https/logs; curl -s --unix-socket /srv/berth/caddy/run/admin.sock http://x/config/logging/logs/ | head -c 1500`))
    // tamper protection: locked except uploads
    const tp = await change('site.tamper', { id: site.result.id, on: true, writable: ['uploads'] })
    assert(tp.status === 'done', 'tamper on')
    assert(onHost(`echo x > /srv/berth/caddy/sites/${D}/index.html 2>&1 || echo denied`).includes('denied') || onHost(`cat /srv/berth/caddy/sites/${D}/index.html`) === 'hi', 'files locked even for root')
    assert(onHost(`echo up > /srv/berth/caddy/sites/${D}/uploads/u.txt && cat /srv/berth/caddy/sites/${D}/uploads/u.txt`) === 'up', 'uploads writable')
    const off = await post(`/api/v1/changes/${tp.id}/revert`, {})
    assert(off.status === 200, 'tamper undo')
    for (let i = 0; i < 10; i++) { if ((await api(`/api/v1/changes/${off.body.id}`)).body.status === 'done') break; await sleep(500) }
    assert(onHost(`echo y > /srv/berth/caddy/sites/${D}/index.html && cat /srv/berth/caddy/sites/${D}/index.html`) === 'y', 'unlocked again')
    await page.goto(`${BASE}/local/sites/s/${site.result.id}`)
    await click('::-p-xpath(//button[normalize-space(.)="防护"])')
    await waitText('防盗链', 10000)
    await page.screenshot({ path: OUT + 'site-protect.png' })
  } finally {
    onHost(`chattr -R -i /srv/berth/caddy/sites/${D} 2>/dev/null; true`)
    await change('site.delete', { id: site.result.id })
    onHost(`rm -rf /srv/berth/caddy/sites/${D}; [ -f /srv/berth/caddy/logs/${D}.log ] && : > /srv/berth/caddy/logs/${D}.log; true`) // emptied, not removed: Caddy may keep the file open
  }
})

await spec('db-import-export', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitC = async (c) => { for (let i = 0; i < 120 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const cand = (await api('/api/v1/hosts/local/databases')).body.candidates.find((c) => c.engine === 'postgres')
  assert(cand, 'a postgres container to use')
  const envs = onHost(`docker inspect ${cand.container} -f '{{range .Config.Env}}{{println .}}{{end}}'`)
  const pick = (k) => (envs.split('\n').find((l) => l.startsWith(k + '=')) ?? '').slice(k.length + 1)
  const inst = await change('db.instance.add', { name: 'e2epg', engine: 'postgres', container: cand.container, user: pick('POSTGRES_USER') || 'postgres', password: pick('POSTGRES_PASSWORD') })
  assert(inst.status === 'done', 'instance: ' + JSON.stringify(inst.result))
  const id = inst.result.id
  try {
    assert((await change('db.create', { instance: id, name: 'e2eimp' })).status === 'done', 'db created')
    onHost(`printf 'CREATE TABLE t (n int); INSERT INTO t VALUES (1),(2),(3);\\n' | gzip > /tmp/e2e-imp.sql.gz`)
    const imp = await waitC(await change('db.import', { instance: id, database: 'e2eimp', path: '/tmp/e2e-imp.sql.gz' }))
    assert(imp.status === 'done', 'import: ' + JSON.stringify(imp.result))
    const q = await post(`/api/v1/hosts/local/databases/${id}/query`, { database: 'e2eimp', sql: 'select count(*) from t' })
    assert(q.status === 200 && String(q.body.rows[0][0]) === '3', 'rows imported: ' + JSON.stringify(q.body))
    const ex = await waitC(await change('db.export', { instance: id, database: 'e2eimp' }))
    assert(ex.status === 'done' && onHost(`zcat ${ex.result.path} | grep -c 'CREATE TABLE'`) !== '0', 'exported')
    // undo the import: the copy taken before it comes back (no table t)
    const un = await waitC((await post(`/api/v1/changes/${imp.id}/revert`, {})).body)
    assert(un.status === 'done', 'undo import: ' + JSON.stringify(un.result ?? un).slice(0, 500) + (un.log || '').slice(-300))
    const q2 = await post(`/api/v1/hosts/local/databases/${id}/query`, { database: 'e2eimp', sql: "select count(*) from information_schema.tables where table_name='t'" })
    assert(String(q2.body.rows[0][0]) === '0', 'table gone after undo: ' + JSON.stringify(q2.body))
  } finally {
    await change('db.drop', { instance: id, name: 'e2eimp' })
    await change('db.instance.remove', { instance: id })
    onHost('rm -f /tmp/e2e-imp.sql.gz /srv/berth/backups/db-drops/e2epg-*')
  }
})

await spec('ops-extras', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitC = async (c) => { for (let i = 0; i < 120 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  onHost('rm -rf /tmp/e2e-fetch && mkdir -p /tmp/e2e-fetch')
  const f = await waitC(await change('file.fetch', { url: 'https://www.example.com/', path: '/tmp/e2e-fetch', name: 'example.html' }))
  assert(f.status === 'done' && onHost('grep -c "Example Domain" /tmp/e2e-fetch/example.html') !== '0', 'remote download: ' + JSON.stringify(f.result))
  const again = await post('/api/v1/hosts/local/changes/file.fetch', { url: 'https://www.example.com/', path: '/tmp/e2e-fetch', name: 'example.html' })
  const againC = await waitC(again.body)
  assert(againC.status === 'failed', 'an existing file is not overwritten')
  await waitC((await post(`/api/v1/changes/${f.id}/revert`, {})).body)
  assert(onHost('test -e /tmp/e2e-fetch/example.html && echo y || echo n') === 'n', 'undo moved it to the trash')
  onHost('rm -rf /tmp/e2e-fetch')
  const lg = await waitC(await change('docker.logs', { max_size: '10m' }))
  assert(lg.status === 'done' && onHost('cat /etc/docker/daemon.json').includes('"max-size": "10m"'), 'log cap set')
  await waitC((await post(`/api/v1/changes/${lg.id}/revert`, {})).body)
  assert(!onHost('cat /etc/docker/daemon.json').includes('max-size'), 'log cap removed')
})

await spec('panel-snapshot', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const dir = OUT + 'dl/'
  mkdirSync(dir, { recursive: true })
  for (const f of readdirSync(dir)) unlinkSync(dir + f)
  const cdp = await page.createCDPSession()
  await cdp.send('Browser.setDownloadBehavior', { behavior: 'allow', downloadPath: dir })
  const weak = await post('/api/v1/panel/snapshot', { password: 'short' })
  assert(weak.status === 400 || weak.status === 403, 'weak password refused: ' + weak.status)
  await post('/api/v1/me/reauth', { password: PASSWORD })
  await page.goto(`${BASE}/local/settings`)
  await waitText('面板快照（换机器）')
  const snapPw = 'e2e-snap-' + Math.random().toString(36).slice(2)
  await fill('加密密码', snapPw)
  await fill('再输一次', snapPw)
  await click(btn('生成并下载快照'))
  await waitText('已下载', 60000)
  let file = ''
  for (let i = 0; i < 30 && !file; i++) { file = readdirSync(dir).find((f) => f.endsWith('.berthsnap')) ?? ''; if (!file) await sleep(500) }
  assert(file, 'snapshot downloaded')
  const head = readFileSync(dir + file).subarray(0, 10).toString()
  assert(head === 'BERTHSNAP1', 'sealed file: ' + head)
  sh('scp', ['-q', dir + file, HOST + ':/tmp/e2e.berthsnap'])
  const ok = onHost(`BERTH_SNAPSHOT_PASSWORD='${snapPw}' /usr/local/bin/berth restore-panel --check /tmp/e2e.berthsnap`)
  assert(ok.includes('snapshot ok') && ok.includes('panel database included'), 'opens with the right password: ' + ok)
  const bad = onHost(`BERTH_SNAPSHOT_PASSWORD='wrong-password' /usr/local/bin/berth restore-panel --check /tmp/e2e.berthsnap 2>&1; echo rc=$?`)
  assert(!bad.includes('snapshot ok') && !bad.includes('rc=0'), 'wrong password fails: ' + bad)
  onHost('rm -f /tmp/e2e.berthsnap; ls /var/lib/berth-e2e/panel-snapshot.db 2>/dev/null; true') === '' || assert(false, 'temporary db removed')
  const audit = (await api('/api/v1/changes?limit=20')).body
  assert(JSON.stringify(audit).includes('panel.snapshot'), 'recorded in the audit log')
  for (const f of readdirSync(dir)) unlinkSync(dir + f)
})

await spec('sftp', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitC = async (c) => { for (let i = 0; i < 120 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const U = 'e2esftp', DIR = '/srv/e2e-sftp'
  onHost(`userdel ${U} 2>/dev/null; rm -rf ${DIR} /tmp/e2e-sftp-key*; mkdir -p ${DIR}/sub && echo hello > ${DIR}/hello.txt && ssh-keygen -q -t ed25519 -N '' -f /tmp/e2e-sftp-key`)
  const rootBefore = onHost('sshd -T -C user=root,host=localhost,addr=127.0.0.1 | grep -E "^(port|permitrootlogin|passwordauthentication|subsystem)" | sort')
  const pw = 'e2e-' + Math.random().toString(36).slice(2) + 'Sf'
  const pub = onHost('cat /tmp/e2e-sftp-key.pub')
  try {
    const c = await waitC(await change('sftp.set', { name: U, dir: DIR, password: pw, keys: [pub] }))
    assert(c.status === 'done', 'created: ' + JSON.stringify(c.result ?? c.error))
    const list = (await api('/api/v1/hosts/local/sftp')).body.items
    assert(list.some((a) => a.name === U && a.mounted && a.has_keys === 1), 'listed and mounted: ' + JSON.stringify(list))
    assert(onHost('sshd -T -C user=root,host=localhost,addr=127.0.0.1 | grep -E "^(port|permitrootlogin|passwordauthentication|subsystem)" | sort') === rootBefore, "root's sshd settings unchanged")
    const sftpKey = (batch) => onHost(`printf '${batch}\\n' | sftp -q -b - -i /tmp/e2e-sftp-key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes ${U}@127.0.0.1 2>&1; echo rc=$?`)
    const ls = sftpKey('pwd\nls /\nls')
    assert(ls.includes('hello.txt') && ls.includes('rc=0') && !ls.includes('etc'), 'key login sees only the directory: ' + ls + sftpKey('ls -la') + onHost(`ls -laZ ${DIR}; getfacl ${DIR}/hello.txt; ausearch -m avc -ts recent 2>/dev/null | tail -5`))
    const put = sftpKey('put /etc/hostname up.txt')
    assert(put.includes('rc=0') && onHost(`cat ${DIR}/up.txt`) !== '', 'upload works: ' + put)
    // password login through SSH_ASKPASS (no sshpass on the box)
    onHost(`printf '#!/bin/sh\\necho "${pw}"\\n' > /tmp/e2e-askpass && chmod 700 /tmp/e2e-askpass`)
    const byPw = onHost(`printf 'ls\\n' | SSH_ASKPASS=/tmp/e2e-askpass SSH_ASKPASS_REQUIRE=force setsid -w sftp -q -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o PubkeyAuthentication=no ${U}@127.0.0.1 2>&1; echo rc=$?`)
    assert(byPw.includes('hello.txt') && byPw.includes('rc=0'), 'password login (sftp -b would force BatchMode, so commands come on stdin): ' + byPw)
    const shell = onHost(`ssh -i /tmp/e2e-sftp-key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes ${U}@127.0.0.1 id 2>&1; echo rc=$?`)
    assert(!shell.includes('uid='), 'no shell: ' + shell)
    // read only: uploads refused
    const ro = await waitC(await change('sftp.set', { name: U, dir: DIR, readonly: true }))
    assert(ro.status === 'done', 'read only: ' + JSON.stringify(ro.result ?? ro.error))
    const put2 = sftpKey('put /etc/hostname up2.txt')
    assert(!put2.includes('rc=0') && onHost(`test -e ${DIR}/up2.txt && echo y || echo n`) === 'n', 'read only refuses uploads: ' + put2)
    // a system directory is refused
    const bad = await post('/api/v1/hosts/local/changes/sftp.set', { name: 'e2ebad', dir: '/etc', password: pw })
    const badC = bad.body?.status ? await waitC(bad.body) : bad.body
    assert(JSON.stringify(badC).includes('sftp_dir_denied') || badC?.status === 'failed', '/etc refused: ' + JSON.stringify(badC).slice(0, 200))
    // the UI lists it
    await page.goto(`${BASE}/local/files${DIR}`)
    await click(btn('SFTP 账号'))
    await waitText(`${U}@`)
    const d = await waitC(await change('sftp.delete', { name: U }))
    assert(d.status === 'done', 'deleted: ' + JSON.stringify(d.result ?? d.error))
    assert(onHost(`id ${U} >/dev/null 2>&1 && echo y || echo n; findmnt /srv/berth/sftp/${U}/files >/dev/null && echo m || echo -; cat ${DIR}/hello.txt; test -e /etc/ssh/sshd_config.d/90-berth-sftp.conf && echo conf || echo noconf; semodule -l | grep -c '^berth_sftp$' || true`) === 'n\n-\nhello\nnoconf\n0', 'user, mount, sshd drop-in and SELinux module gone, files kept')
  } finally {
    if ((await api('/api/v1/hosts/local/sftp')).body.items.some((a) => a.name === U)) await waitC(await change('sftp.delete', { name: U }))
    onHost(`rm -rf ${DIR} /tmp/e2e-sftp-key* /tmp/e2e-askpass`)
  }
})

await spec('db-remote-slow', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitC = async (c) => { for (let i = 0; i < 300 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const N = 'e2edbx'
  onHost(`rm -rf /srv/berth/apps/${N}`)
  const app = await waitC(await change('app.install', { app: 'mariadb', name: N, values: { DB_PORT: '13306' } }))
  assert(app.status === 'done', 'mariadb installed: ' + JSON.stringify(app.result ?? app.error).slice(0, 300))
  let inst, pgInst
  try {
    const cont = (await api('/api/v1/hosts/local/databases')).body.candidates.find((x) => x.container.startsWith(N))
    for (let i = 0; i < 30; i++) { inst = await change('db.instance.add', { name: N, engine: 'mariadb', container: cont.container }); if (inst.status === 'done') break; await sleep(2000) }
    assert(inst?.status === 'done', 'instance: ' + JSON.stringify(inst?.result ?? inst))
    const id = inst.result.id
    // remote access: local only → a LAN address → undo back
    const r0 = (await api(`/api/v1/hosts/local/databases/${id}/remote`)).body
    assert(r0.bind === '127.0.0.1' && r0.port === 13306 && r0.addrs.length > 0, 'starts local only: ' + JSON.stringify(r0))
    const lan = r0.addrs[0]
    const on = await waitC(await change('db.remote', { instance: id, bind: lan }))
    assert(on.status === 'done', 'remote on: ' + JSON.stringify(on.result ?? on.error))
    assert(onHost(`ss -ltnH 'sport = :13306' | awk '{print $4}' | sort -u`).includes(lan + ':13306'), 'listens on ' + lan)
    assert((await api(`/api/v1/hosts/local/databases/${id}/remote`)).body.bind === lan, 'state shows the LAN address')
    const bad = await post('/api/v1/hosts/local/changes/db.remote', { instance: id, bind: '203.0.113.9' })
    assert(bad.status >= 400 || JSON.stringify(bad.body).includes('bad_bind'), 'a foreign address is refused')
    await waitC((await post(`/api/v1/changes/${on.id}/revert`, {})).body)
    assert(onHost(`ss -ltnH 'sport = :13306' | awk '{print $4}' | sort -u`) === '127.0.0.1:13306', 'undo: local only again')
    // slow query log
    const sl = await waitC(await change('db.slowlog', { instance: id, on: true, seconds: 0.1 }))
    assert(sl.status === 'done', 'slow log on: ' + JSON.stringify(sl.result ?? sl.error))
    await waitC(await change('db.exec', { instance: id, sql: 'SELECT SLEEP(0.3) AS e2e_slow' }))
    let st
    for (let i = 0; i < 10; i++) { st = (await api(`/api/v1/hosts/local/databases/${id}/slowlog`)).body; if (st.items.some((x) => x.sql.includes('e2e_slow'))) break; await sleep(1000) }
    assert(st.on && st.seconds === 0.1 && st.items.some((x) => x.sql.includes('e2e_slow') && x.ms >= 250), 'slow statement listed: ' + JSON.stringify(st).slice(0, 400))
    // MariaDB forgets SET GLOBAL on restart; the setting is kept for Berth to put back
    await waitC((await post(`/api/v1/changes/${sl.id}/revert`, {})).body)
    assert(!(await api(`/api/v1/hosts/local/databases/${id}/slowlog`)).body.on, 'undo: slow log off')
    // the UI tabs
    await page.goto(`${BASE}/local/db`)
    await click(`::-p-text(${N})`)
    await click('::-p-text(远程访问)')
    await waitText('只在本机（关闭远程访问）')
    await click('::-p-text(慢查询)')
    await waitText('记录慢查询')
    // postgres: ALTER SYSTEM, slow statements from the container log
    const cand = (await api('/api/v1/hosts/local/databases')).body.candidates.find((c) => c.engine === 'postgres')
    if (cand) {
      const envs = onHost(`docker inspect ${cand.container} -f '{{range .Config.Env}}{{println .}}{{end}}'`)
      const pick = (k) => (envs.split('\n').find((l) => l.startsWith(k + '=')) ?? '').slice(k.length + 1)
      pgInst = await change('db.instance.add', { name: 'e2epgslow', engine: 'postgres', container: cand.container, user: pick('POSTGRES_USER') || 'postgres', password: pick('POSTGRES_PASSWORD') })
      assert(pgInst.status === 'done', 'pg instance')
      const ps = await waitC(await change('db.slowlog', { instance: pgInst.result.id, on: true, seconds: 0.1 }))
      assert(ps.status === 'done', 'pg slow on: ' + JSON.stringify(ps.result ?? ps.error))
      await waitC(await change('db.exec', { instance: pgInst.result.id, sql: 'SELECT pg_sleep(0.3), 1 AS e2e_pgslow' }))
      let ss
      for (let i = 0; i < 10; i++) { ss = (await api(`/api/v1/hosts/local/databases/${pgInst.result.id}/slowlog`)).body; if (ss.items.some((x) => x.sql.includes('e2e_pgslow'))) break; await sleep(1000) }
      assert(ss.on && ss.items.some((x) => x.sql.includes('e2e_pgslow')), 'pg slow statement listed: ' + JSON.stringify(ss).slice(0, 400))
      await waitC(await change('db.slowlog', { instance: pgInst.result.id, on: false }))
      assert(!(await api(`/api/v1/hosts/local/databases/${pgInst.result.id}/slowlog`)).body.on, 'pg slow log off')
    }
  } finally {
    if (pgInst?.result?.id) await change('db.instance.remove', { instance: pgInst.result.id })
    if (inst?.result?.id) await change('db.instance.remove', { instance: inst.result.id })
    await waitC(await change('app.remove', { name: N }))
    onHost(`rm -rf /srv/berth/apps/${N}`)
  }
})

await spec('net-vol', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitC = async (c) => { for (let i = 0; i < 60 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const last = async (kind) => { let c; for (let i = 0; i < 30; i++) { c = (await api(`/api/v1/changes?kind=${kind}`)).body.items[0]; if (c && !['running', 'queued', 'planned'].includes(c.status)) break; await sleep(1000) } return c }
  onHost('docker network rm e2e-net >/dev/null 2>&1; docker volume rm e2e-vol >/dev/null 2>&1; true')
  try {
    await page.goto(`${BASE}/local/containers/networks`)
    await fill('网络名称', 'e2e-net')
    await fill('网段（留空自动分配）', '172.31.250.0/24')
    await click('::-p-text(不能访问外网)')
    await click(btn('新建网络'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    const n = await last('network.create')
    assert(n?.status === 'done', 'network: ' + JSON.stringify(n).slice(0, 300))
    assert(onHost(`docker network inspect e2e-net -f '{{.Internal}} {{(index .IPAM.Config 0).Subnet}}'`) === 'true 172.31.250.0/24', 'internal network on the subnet')
    await waitText('172.31.250.0/24', 10000)
    await page.goto(`${BASE}/local/containers/volumes`)
    await fill('存储卷名称', 'e2e-vol')
    await click(btn('新建存储卷'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    const v = await last('volume.create')
    assert(v?.status === 'done', 'volume: ' + JSON.stringify(v).slice(0, 300))
    assert(onHost(`docker volume inspect e2e-vol -f '{{.Name}}'`) === 'e2e-vol', 'volume exists')
    const bad = await post('/api/v1/hosts/local/changes/network.create', { name: 'bad name!' })
    assert(bad.status >= 400 || (await waitC(bad.body)).status === 'failed', 'bad name refused')
    for (const c of [n, v]) assert((await waitC((await post(`/api/v1/changes/${c.id}/revert`, {})).body)).status === 'done', 'undo ' + c.kind)
    assert(onHost(`docker network ls -q -f name=^e2e-net$`) === '' && onHost(`docker volume ls -q -f name=^e2e-vol$`) === '', 'undo removed both')
  } finally {
    onHost('docker network rm e2e-net >/dev/null 2>&1; docker volume rm e2e-vol >/dev/null 2>&1; true')
  }
})

await spec('db-root-reset', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const waitC = async (c) => { for (let i = 0; i < 300 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const N = 'e2emy'
  onHost(`rm -rf /srv/berth/apps/${N}`)
  const app = await waitC(await change('app.install', { app: 'mysql', name: N, values: { DB_PORT: '13307' } }))
  assert(app.status === 'done', 'mysql installed: ' + JSON.stringify(app.result ?? app.error).slice(0, 300))
  let inst, pgInst
  try {
    const cont = (await api('/api/v1/hosts/local/databases')).body.candidates.find((x) => x.container.startsWith(N))
    for (let i = 0; i < 40; i++) { inst = await change('db.instance.add', { name: N, engine: 'mysql', container: cont.container }); if (inst.status === 'done') break; await sleep(3000) }
    assert(inst?.status === 'done', 'instance: ' + JSON.stringify(inst?.result ?? inst))
    // MySQL: from the instance drawer, without the old password
    await page.goto(`${BASE}/local/db`)
    await click(`::-p-text(${N})`)
    await waitText('sys', 15000)
    await sleep(1500)
    await click(btn('重置管理员密码'))
    const pwEl = await page.waitForSelector('::-p-aria([name="新密码"][role="textbox"])')
    const pw = await pwEl.evaluate((e) => e.value)
    assert(pw.length >= 16, 'a password is generated')
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="下一步"])')
    await waitText('不需要旧密码', 15000)
    await click('[data-testid=confirm]:not([disabled])', 15000)
    let c
    for (let i = 0; i < 150; i++) { c = (await api('/api/v1/changes?kind=db.root_reset')).body.items[0]; if (c && !['running', 'queued', 'planned'].includes(c.status)) break; await sleep(1000) }
    assert(c?.status === 'done', 'mysql reset: ' + JSON.stringify(c).slice(0, 500))
    assert(onHost(`docker exec ${cont.container} mysql -uroot -p'${pw}' -N -e 'SELECT 42' 2>/dev/null`).includes('42'), 'root logs in with the new password')
    assert(onHost(`grep ^ROOT_PASSWORD= /srv/berth/stacks/${N}/.env`) === 'ROOT_PASSWORD=' + pw, '.env carries the new password')
    assert(!JSON.stringify(c).includes(pw), 'the password is not in the audit record')
    onHost(`docker ps -a --format '{{.Names}}' | grep -q berth-dbreset && exit 1 || true`)
    // PostgreSQL over the local socket: change it and put the fixture's password back
    const cand = (await api('/api/v1/hosts/local/databases')).body.candidates.find((x) => x.engine === 'postgres')
    if (cand) {
      const envs = onHost(`docker inspect ${cand.container} -f '{{range .Config.Env}}{{println .}}{{end}}'`)
      const pick = (k) => (envs.split('\n').find((l) => l.startsWith(k + '=')) ?? '').slice(k.length + 1)
      const orig = pick('POSTGRES_PASSWORD')
      pgInst = await change('db.instance.add', { name: 'e2epgreset', engine: 'postgres', container: cand.container, user: pick('POSTGRES_USER') || 'postgres', password: orig })
      assert(pgInst.status === 'done', 'pg instance')
      const r1 = await waitC(await change('db.root_reset', { instance: pgInst.result.id, password: 'E2eTempPass_123' }))
      assert(r1.status === 'done', 'pg reset: ' + JSON.stringify(r1.result ?? r1.error).slice(0, 300))
      const r2 = await waitC(await change('db.root_reset', { instance: pgInst.result.id, password: orig }))
      assert(r2.status === 'done', 'pg reset back: ' + JSON.stringify(r2.result ?? r2.error).slice(0, 300))
    }
  } finally {
    if (pgInst?.result?.id) await change('db.instance.remove', { instance: pgInst.result.id })
    if (inst?.result?.id) await change('db.instance.remove', { instance: inst.result.id })
    await waitC(await change('app.remove', { name: N }))
    onHost(`docker rm -f berth-dbreset-${N}-mysql-1 >/dev/null 2>&1; rm -rf /srv/berth/apps/${N}; true`)
  }
})

// R5: the WAF and virus scanning are optional extensions. A host that already used them counts
// as installed (no migration step); removing takes everything away (the WAF's sites are turned
// off first, and undo turns them back on); a site cannot turn the WAF on without it.
await spec('builtin-ext', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd], { timeout: 900000 })
  const waitC = async (c) => { for (let i = 0; i < 1200 && ['running', 'queued', 'planned'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const st = async () => (await api('/api/v1/hosts/local/extensions/builtin')).body
  const D = 'waf-e2e.berth.test'
  const curl = (q) => onHost(`curl -sk -o /dev/null -w '%{http_code}' --max-time 10 --resolve ${D}:443:127.0.0.1 'https://${D}/${q}' || true`)
  let site, s0 = await st()
  console.log('    before:', JSON.stringify({ waf: s0.waf.installed, image: s0.waf.image, av: s0.av.installed }))
  try {
    // an old edge (built with Coraza) and a ClamAV image already here count as installed
    if (/^berth\/caddy:2\.11-[12]$/.test(s0.waf.image)) assert(s0.waf.installed, 'an older edge counts as having the WAF')
    // ---- WAF: start from not installed ----
    if (s0.waf.installed) {
      const rm = await waitC(await change('ext.waf.remove', {}))
      assert(rm.status === 'done', 'waf removed: ' + JSON.stringify(rm.error))
    }
    for (let i = 0; i < 30 && (await st()).waf.image !== 'berth/caddy:2.11-3'; i++) await sleep(2000)
    assert(!(await st()).waf.installed && onHost('docker inspect -f {{.Config.Image}} berth-caddy') === 'berth/caddy:2.11-3', 'the edge runs without the WAF')
    const refused = await post('/api/v1/hosts/local/changes/site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'static' }, options: { waf: { mode: 'block' } } })
    assert(JSON.stringify(refused.body).includes('waf.not_installed'), 'a site cannot turn the WAF on without it: ' + JSON.stringify(refused.body).slice(0, 200))
    site = await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'static' } })
    assert(site.status === 'done', 'site')
    await page.goto(`${BASE}/local/extensions`)
    await page.waitForFunction(() => location.pathname === '/local/plugins', { timeout: 10000 })
    await page.goto(`${BASE}/local/plugins/official`)
    await waitText('Berth 官方扩展', 10000)
    await waitText('网站防火墙（WAF）')
    await waitText('未安装', 10000)
    await page.screenshot({ path: OUT + 'builtin-ext.png' })
    // install, turn it on for the site, attacks are blocked
    const t0 = Date.now()
    const ins = await waitC(await change('ext.waf.install', {}))
    console.log(`    waf install: ${ins.status} in ${Math.round((Date.now() - t0) / 1000)} s`)
    assert(ins.status === 'done' && (await st()).waf.installed, 'waf installed: ' + JSON.stringify(ins.error))
    const cur = (await api(`/api/v1/hosts/local/sites/${site.result.id}`)).body.site
    const up = await change('site.update', { ...cur, options: { ...cur.options, waf: { mode: 'block' } } })
    assert(up.status === 'done', 'waf on: ' + JSON.stringify(up.error))
    let code = ''
    for (let i = 0; i < 20 && code !== '403'; i++) { code = curl("?id=1'%20OR%20'1'='1"); if (code !== '403') await sleep(1000) }
    const normal = curl('')
    assert(code === '403' && /^[23]|404/.test(normal), 'an injection is blocked, a normal request is not: ' + code + ' / ' + normal)
    // removal turns the site's WAF off; undo puts it back
    const rm = await waitC(await change('ext.waf.remove', {}))
    assert(rm.status === 'done' && rm.result.sites >= 1, 'removed: ' + JSON.stringify(rm.error ?? rm.result))
    assert(!(await api(`/api/v1/hosts/local/sites/${site.result.id}`)).body.site.options.waf, 'the site\'s WAF went off')
    let after = ''
    for (let i = 0; i < 20 && !/^(2|3|404)/.test(after = curl("?id=1'%20OR%20'1'='1")); i++) await sleep(1000)
    assert(/^(2|3|404)/.test(after), 'the site still serves, without the WAF: ' + after)
    const un = await waitC((await post(`/api/v1/changes/${rm.id}/revert`, {})).body)
    assert(un.status === 'done' && (await api(`/api/v1/hosts/local/sites/${site.result.id}`)).body.site.options.waf?.mode === 'block', 'undo: installed again, the site\'s WAF back: ' + JSON.stringify({ st: un.status, e: un.error, r: un.result }).slice(0, 400))
    for (let i = 0; i < 20 && curl("?id=1'%20OR%20'1'='1") !== '403'; i++) await sleep(1000)
    assert(curl("?id=1'%20OR%20'1'='1") === '403', 'and blocking again')

    // ---- virus scanning ----
    if ((await st()).av.installed) {
      const r = await waitC(await change('ext.av.remove', {}))
      assert(r.status === 'done', 'av removed: ' + JSON.stringify(r.error))
    }
    assert(onHost('docker image inspect clamav/clamav-debian:stable >/dev/null 2>&1 && echo y || echo n') === 'n' && onHost('docker volume inspect berth-clamav-db >/dev/null 2>&1 && echo y || echo n') === 'n', 'removal leaves no image or signatures')
    const no = await post('/api/v1/hosts/local/changes/av.scan', { paths: ['/srv/berth/caddy/sites'] })
    assert(JSON.stringify(no.body).includes('av.not_installed'), 'scanning asks for the extension: ' + JSON.stringify(no.body).slice(0, 200))
    const a0 = Date.now()
    const ai = await waitC(await change('ext.av.install', {}))
    console.log(`    av install: ${ai.status} in ${Math.round((Date.now() - a0) / 1000)} s`)
    assert(ai.status === 'done' && (await st()).av.installed && (await st()).av.db_date, 'av installed with signatures: ' + JSON.stringify(ai.error))
  } finally {
    if (site?.result?.id) await change('site.delete', { id: site.result.id })
    onHost(`rm -rf /srv/berth/caddy/sites/${D}`)
    // leave the host as it was: the WAF on its edge, ClamAV present
    if (!(await st()).waf.installed) await waitC(await change('ext.waf.install', {}))
    if (!(await st()).av.installed) await waitC(await change('ext.av.install', {}))
  }
})

await spec('virus-scan', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const DIR = '/srv/e2e-av'
  // the EICAR test string, put together on the host so the repository never holds it whole
  const a = 'X5O!P%@AP[4\\PZX54(P^)7CC)7}$EICAR', b = '-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*'
  onHost(`rm -rf ${DIR}; mkdir -p ${DIR}/sub && printf '%s%s' '${a}' '${b}' > ${DIR}/sub/bad.php && echo '<?php echo 1;' > ${DIR}/index.php`)
  try {
    await page.goto(`${BASE}/local/files${DIR}`)
    await waitText('index.php')
    await click(btn('扫描病毒'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    await waitText('发现 1 个可疑文件', 300000)
    await waitText(`${DIR}/sub/bad.php`)
    await waitText('Eicar')
    await click(btn('全部移到回收站（1）'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    await waitText('已移到回收站', 30000)
    assert(onHost(`test -e ${DIR}/sub/bad.php && echo y || echo n; test -e ${DIR}/index.php && echo y || echo n`) === 'n\ny', 'the infected file went to the trash, the clean one stayed')
    const c = (await api('/api/v1/changes?kind=av.scan&limit=1')).body.items?.[0]
    assert(c?.status === 'done' && c.result?.files >= 2, 'scan recorded: ' + JSON.stringify(c?.result).slice(0, 200))
    const bad = await post('/api/v1/hosts/local/changes/av.scan', { paths: ['/proc'] })
    assert(JSON.stringify(bad.body).includes('av.bad_path') || bad.status >= 400 || bad.body?.status === 'failed', '/proc refused')
  } finally {
    onHost(`rm -rf ${DIR}`)
    const tr = (await api('/api/v1/hosts/local/files/trash')).body?.items ?? []
    for (const x of tr.filter((x) => (x.paths ?? []).some((p) => p.startsWith(DIR)))) await change('file.purge', { id: x.id })
  }
})

await spec('runtimes', async () => {
  const waitC = async (c) => { for (let i = 0; i < 600 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const has20 = () => api('/api/v1/hosts/local/runtimes').then((r) => r.body.items.find((x) => x.tool === 'node' && x.version.startsWith('20.')))
  assert(!(await has20()), 'node 20 not there before')
  await page.goto(`${BASE}/local/projects/runtimes`)
  await waitText('运行环境')
  const c = await waitC(await change('runtime.install', { tool: 'node', version: '20' }))
  assert(c.status === 'done', 'installed: ' + JSON.stringify(c.result ?? c.error).slice(0, 300))
  const x = await has20()
  assert(x && x.bytes > 10 << 20 && x.used_by.length === 0, 'listed with its size: ' + JSON.stringify(x))
  await page.reload()
  await waitText(x.version)
  await waitText('没有项目在用')
  const u = await waitC((await post(`/api/v1/changes/${c.id}/revert`, {})).body)
  assert(u.status === 'done' && !(await has20()), 'undo removed it: ' + JSON.stringify(u.result ?? u.error))
})

await spec('packages', async () => {
  const waitC = async (c) => { for (let i = 0; i < 300 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  sh('ssh', [HOST, 'dnf -y -q remove figlet >/dev/null 2>&1; true'])
  const snapsBefore = new Set(((await api('/api/v1/hosts/local/system/snapshots')).body.snapshots ?? []).map((x) => x.id))
  try {
    const bad = await post('/api/v1/hosts/local/changes/pkg.remove?plan=1', { names: ['openssh-server'] })
    assert(bad.status >= 400 && JSON.stringify(bad.body).includes('pkg.protected'), 'refuses to remove sshd: ' + JSON.stringify(bad.body).slice(0, 300))
    const none = await post('/api/v1/hosts/local/changes/pkg.install?plan=1', { names: ['no-such-pkg-e2e'] })
    assert(none.status === 404, 'unknown package: ' + JSON.stringify(none.body).slice(0, 300))
    await page.goto(`${BASE}/local/system?tab=packages`)
    await waitText('手动安装的软件包')
    await fill('[name="搜索软件包"][role="textbox"]', 'figlet')
    await page.waitForSelector('::-p-xpath(//div[.//span[normalize-space(.)="figlet"]]/following-sibling::button[normalize-space(.)="安装"])', { timeout: 30000 })
    await click('::-p-xpath(//div[.//span[normalize-space(.)="figlet"]]/following-sibling::button[normalize-space(.)="安装"])')
    await waitText('连同依赖共', 30000)
    await waitText('+ figlet')
    await click('[data-testid=confirm]:not([disabled])', 15000)
    let id
    for (let i = 0; i < 120 && !id; i++) { await sleep(1000); id = (await api('/api/v1/changes?kind=pkg.install')).body.items?.find((c) => c.status === 'done')?.id }
    assert(id, 'install finished')
    assert(sh('ssh', [HOST, 'rpm -q figlet; true']).startsWith('figlet-'), 'figlet installed')
    await page.waitForSelector('::-p-xpath(//div[.//span[normalize-space(.)="figlet"]]/following-sibling::button[normalize-space(.)="卸载"])', { timeout: 20000 })
    await page.screenshot({ path: OUT + 'packages.png' })
    const u = await waitC((await post(`/api/v1/changes/${id}/revert`, {})).body)
    assert(u.status === 'done', 'undo: ' + JSON.stringify(u.result ?? u.error).slice(0, 300))
    assert(sh('ssh', [HOST, 'rpm -q figlet; true']).includes('not installed'), 'undo removed figlet')
  } finally {
    sh('ssh', [HOST, 'dnf -y -q remove figlet >/dev/null 2>&1; true'])
    // installing and removing took automatic system snapshots: not this test's to keep
    for (const x of (await api('/api/v1/hosts/local/system/snapshots')).body.snapshots ?? []) if (!snapsBefore.has(x.id)) await change('system.snapshot.delete', { id: x.id })
  }
})

await spec('ddns', async () => {
  // a "custom update URL" account aimed at a tiny server on the dev box, which logs what it is asked
  sh('ssh', [HOST, 'systemctl stop e2e-ddns 2>/dev/null; systemctl reset-failed e2e-ddns 2>/dev/null; mkdir -p /tmp/e2e-ddns && echo ok > /tmp/e2e-ddns/ok.txt; true'])
  sh('ssh', [HOST, 'systemd-run --unit e2e-ddns -p RuntimeMaxSec=300 /usr/bin/python3 -m http.server 18999 --bind 127.0.0.1 --directory /tmp/e2e-ddns >/dev/null 2>&1; sleep 1; true'])
  await page.goto(`${BASE}/local/domains`)
  await click(btn('添加 DNS 账号'))
  await page.waitForSelector('::-p-xpath(//*[@role="dialog"]//select/option[@value="customurl"])')
  await page.select('::-p-xpath(//*[@role="dialog"]//select)', 'customurl')
  await waitText('{ip} 和 {domain}')
  await fill('更新网址', 'http://127.0.0.1:18999/ok.txt?ip={ip}&d={domain}')
  await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"])')
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('自定义更新地址')
  await page.waitForFunction(() => document.querySelectorAll('[role=dialog]').length === 0, { timeout: 15000 })
  await sleep(500)
  await click(btn('添加 DDNS'))
  await fill('记录名', 'e2e.example.test')
  await waitText('走网口 end0 出去的地址')
  await waitText('出口 IP', 30000)
  await click('::-p-xpath(//label[.//div[starts-with(normalize-space(.),"走网口 end0")]]//input)')
  await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"])')
  await click('[data-testid=confirm]:not([disabled])', 30000)
  await page.waitForFunction(async () => {
    const l = (await fetch('/api/v1/ddns').then((r) => r.json())).items
    return l.length > 0 && l[0].last_check > 0
  }, { timeout: 60000, polling: 1000 })
  const pubIP = sh('ssh', [HOST, 'curl -s --max-time 8 https://api.ipify.org; true']).trim()
  const log = sh('ssh', [HOST, 'journalctl -u e2e-ddns --no-pager -o cat; systemctl stop e2e-ddns; true'])
  assert(log.includes(`ip=${pubIP}`) && log.includes('d=e2e.example.test'), 'update URL not called with the right address: ' + log)
  await page.screenshot({ path: OUT + 'ddns.png' })
  const list = await page.evaluate(async () => (await fetch('/api/v1/ddns').then((r) => r.json())).items)
  assert((await change('ddns.delete', { id: list[0].id })).status === 'done', 'ddns delete')
  assert((await change('integration.delete', { id: 'customurl' })).status === 'done', 'account delete')
})

await spec('dev-workspace', async () => {
  const name = 'e2e-ws'
  const ip = HOST.split('@')[1]
  sh('ssh', [HOST, `docker rm -f berth-dev-${name} >/dev/null 2>&1; docker volume rm dev-${name}_home >/dev/null 2>&1; rm -rf /srv/berth/stacks/dev-${name} /srv/berth/dev/${name} /srv/berth/dev/.state/${name}; true`])
  const keyFile = OUT + 'e2e_dev_key'
  sh('rm', ['-f', keyFile, keyFile + '.pub'])
  sh('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-C', 'e2e@berth', '-f', keyFile])
  const pub = readFileSync(keyFile + '.pub', 'utf8').trim()
  await page.goto(`${BASE}/local/dev`)
  await waitText('还没有开发工作区')
  await page.screenshot({ path: OUT + 'dev-empty.png' })
  await click(btn('新建工作区'))
  await click(text('从 Git 克隆'))
  const repo = 'https://github.com/microsoft/vscode-remote-try-node.git'
  for (let i = 0; i < 3 && (await page.$eval('[aria-label="Git 地址"]', (e) => e.value).catch(() => '')) !== repo; i++) await fill('Git 地址', repo)
  await waitText('仓库里有 devcontainer.json 就按它来建')
  await click(text('1 核 · 1 GB'))
  // other specs may have saved keys already: then the wizard offers to add one more
  if (await page.$(btn('再加一把'))) await click(btn('再加一把'))
  await fill('SSH 公钥', pub)
  await click(btn('保存公钥'))
  await page.waitForFunction(() => !document.querySelector('[aria-label="SSH 公钥"]') && /用你保存的 \d+ 把/.test(document.body.innerText), { timeout: 15000 })
  await fill('工作区名字', name)
  await page.screenshot({ path: OUT + 'dev-create.png' })
  await click(btn('创建'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('打开 VS Code', 600000)
  // the record: devcontainer.json was used, the user is the image's own (node)
  let ws
  for (let i = 0; i < 60; i++) {
    ws = (await api('/api/v1/hosts/local/dev')).body.items.find((x) => x.spec.name === name)
    if (ws?.user && ws.user !== 'root') break
    await sleep(2000)
  }
  assert(ws?.spec.devcontainer_path === '.devcontainer/devcontainer.json', 'devcontainer not used: ' + JSON.stringify(ws?.spec))
  assert(ws.user === 'node', 'user ' + ws.user)
  assert(ws.state === 'running', 'state ' + ws.state)
  // SSH with the key pasted in the dialog lands in the project, as that user
  // the SSH server is installed in the container first (apt): give it time
  let out = ''
  for (let i = 0; i < 40 && !out; i++) {
    try {
      out = sh('ssh', ['-i', keyFile, '-p', String(ws.spec.ssh_port), '-o', 'StrictHostKeyChecking=no', '-o', 'UserKnownHostsFile=/dev/null', '-o', 'LogLevel=ERROR', '-o', 'ConnectTimeout=5',
        `node@${ip}`, `cd ${ws.spec.environment.folder} && whoami && ls && node --version && cat /sys/fs/cgroup/cpu.max`])
    } catch { await sleep(3000) }
  }
  assert(out.includes('node\n') && out.includes('package.json') && /v\d+\./.test(out), 'ssh: ' + out)
  assert(out.trim().endsWith('100000 100000'), 'cpu limit not applied: ' + out)
  // web VS Code: HTTPS on the LAN, password sign-in
  const pw = (await api(`/api/v1/hosts/local/dev/${name}/password`)).body.password
  assert(pw?.length >= 12, 'password')
  let login = ''
  for (let i = 0; i < 30 && !login.includes('302'); i++) {
    try { login = sh('curl', ['-sk', '-o', '/dev/null', '-w', '%{http_code}', '-m', '5', '--data-urlencode', `password=${pw}`, `https://${ip}:${ws.spec.web_port}/login`]) } catch { login = '' }
    if (!login.includes('302')) await sleep(2000)
  }
  assert(login === '302', 'code-server login: ' + login)
  const wrong = sh('curl', ['-sk', '-o', '/dev/null', '-w', '%{http_code}', '-m', '5', '--data-urlencode', 'password=nope', `https://${ip}:${ws.spec.web_port}/login`])
  assert(wrong !== '302', 'wrong password accepted')
  // postCreateCommand (npm install) runs once, as the user
  for (let i = 0; i < 90; i++) {
    const st = (await api(`/api/v1/hosts/local/dev/${name}`)).body.status
    const h = st?.hooks?.['post-create']?.status
    if (h === 'done') break
    assert(h !== 'failed', 'postCreate failed: ' + st.hooks['post-create'].log.slice(-400))
    await sleep(2000)
  }
  assert(sh('ssh', [HOST, `test -d /srv/berth/dev/${name}/node_modules && stat -c %u /srv/berth/dev/${name}/package.json`]) === '1000', 'npm install / ownership')
  for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
  await page.screenshot({ path: OUT + 'dev-list.png' })
  await click(`[data-dev=${name}] button.min-w-0`)
  await waitText('连接方式')
  await waitText(`ssh -p ${ws.spec.ssh_port} node@${ip}`)
  await waitText('post-create')
  await page.screenshot({ path: OUT + 'dev-drawer.png' })
  for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
  // the editor itself loads in a browser (its websocket and the workbench), in Chinese
  const ed = await browser.newPage()
  try {
    await ed.setViewport({ width: 1280, height: 800 })
    await ed.goto(`https://${ip}:${ws.spec.web_port}/`, { waitUntil: 'domcontentloaded' })
    const pwBox = await ed.waitForSelector('input[type=password]', { timeout: 20000 })
    await pwBox.type(pw)
    await ed.keyboard.press('Enter')
    await until(ed, () => !!document.querySelector('.monaco-workbench .explorer-folders-view, .monaco-workbench .part.sidebar'), 90000)
    await until(ed, () => document.body.innerText.includes('package.json'), 60000)
    await until(ed, () => document.body.innerText.includes('资源管理器'), 20000) // the language pack took
    await sleep(3000)
    await ed.screenshot({ path: OUT + 'dev-vscode.png' })
  } finally { await ed.close() }
  await page.bringToFront()
  // stop frees it, start brings it back with the same files
  assert((await change('dev.stop', { name })).status === 'done', 'stop')
  assert((await change('dev.start', { name })).status === 'done', 'start')
  // delete with files: container, home volume and folder (to the trash) go
  const rm = await change('dev.delete', { name, files: true })
  assert(rm.status === 'done', 'delete: ' + rm.status)
  assert(sh('ssh', [HOST, `docker ps -a --format '{{.Names}}' | grep -c berth-dev-${name}; test -e /srv/berth/dev/${name} && echo still-there; true`]) === '0', 'leftovers')
})

await spec('claude-workspace', async () => {
  const name = 'e2e-claude'
  const C = 'berth-dev-' + name
  const dir = '/srv/berth/dev/' + name
  sh('ssh', [HOST, `docker rm -f ${C} >/dev/null 2>&1; docker volume rm dev-${name}_home >/dev/null 2>&1; rm -rf /srv/berth/stacks/dev-${name} ${dir} /srv/berth/dev/.state/${name} /tmp/e2e-hook.log; /usr/local/libexec/berth-agentcall -host local POST /v1/dev/${name}/guard '{}' >/dev/null 2>&1; pkill -f "python3 [/]tmp/e2e-hook.py"; true`])
  // a webhook that records notifications (the "done" one must reach the person who sent the task)
  sh('scp', ['-q', ROOT + 'test/e2e/fixtures/hook.py', `${HOST}:/tmp/e2e-hook.py`])
  sh('ssh', [HOST, 'nohup python3 /tmp/e2e-hook.py >/dev/null 2>&1 & sleep 0.5'])
  const ch = await post('/api/v1/me/channels', { name: 'e2e claude hook', config: { kind: 'webhook', url: 'http://127.0.0.1:7098/' } })
  assert(ch.status === 200 && ch.body.categories?.includes?.('dev') !== false, 'channel: ' + JSON.stringify(ch.body))
  // the wizard, with Claude Code switched on
  await page.goto(`${BASE}/local/dev`)
  await click(btn('新建工作区'))
  await click(text('装好 Claude Code（AI 编程助手）'))
  await waitText('所有工作区共用一份')
  await fill('工作区名字', name)
  await click(btn('创建'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  let ws
  for (let i = 0; i < 150; i++) {
    ws = (await api('/api/v1/hosts/local/dev')).body.items.find((x) => x.spec.name === name)
    if (ws?.state === 'running' && ws.user !== 'root') break
    await sleep(2000)
  }
  assert(ws?.spec.claude === true && ws.state === 'running', 'workspace: ' + JSON.stringify(ws?.spec))
  // the real Claude Code is in it (the shared copy), for the workspace's user
  let ver = ''
  for (let i = 0; i < 15 && !ver.includes('Claude Code'); i++) {
    try { ver = sh('ssh', [HOST, `docker exec -u ${ws.user} ${C} claude --version`]) } catch { await sleep(2000) }
  }
  assert(ver.includes('Claude Code'), 'claude --version: ' + ver)
  // with Claude on it is internet only: the internet yes, this LAN and the server no
  assert(ws.spec.no_lan === true, 'no_lan default')
  const ip = HOST.split('@')[1]
  const net = sh('ssh', [HOST, `docker exec ${C} sh -c 'curl -s -o /dev/null -m 8 -w "%{http_code}" https://github.com/ || echo fail; echo; curl -s -o /dev/null -m 4 -w "%{http_code}" http://${ip}:7090/ || echo blocked; echo; curl -s -o /dev/null -m 4 -w "%{http_code}" http://172.17.0.1:7090/ || echo blocked'`])
  const nl = net.split('\n').filter(Boolean)
  assert(/^[1-5]\d\d$/.test(nl[0]) && nl[1].includes('blocked') && nl[2].includes('blocked'), 'egress: ' + JSON.stringify(net))
  let cs = (await api(`/api/v1/hosts/local/dev/${name}/claude`)).body
  assert(cs.enabled && cs.auth === 'login' && cs.state.logged_in === false && cs.state.version, 'claude state: ' + JSON.stringify(cs))
  // a sign-in token through the dialog: sealed, never in the audit trail, readable only by the user
  for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
  await page.goto(`${BASE}/local/dev?ws=${name}`)
  await waitText('还没登录')
  await page.screenshot({ path: OUT + 'claude-drawer.png' })
  await click(btn('登录方式'))
  await click('[role=dialog] ::-p-text(登录令牌)')
  const token = 'sk-ant-oat01-e2e' + Math.random().toString(36).slice(2)
  await fill('密钥或令牌', token)
  await click('[role=dialog] ' + btn('保存'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  for (let i = 0; i < 30 && (await api(`/api/v1/hosts/local/dev/${name}/claude`)).body.auth !== 'token'; i++) await sleep(500)
  // stand-in binary for the tasks (no real account in tests); after the sign-in sync, which relinks the real one
  execFileSync('ssh', [HOST, `docker exec -i -u 0 ${C} sh -c 'rm -f /usr/local/bin/claude && cat >/usr/local/bin/claude && chmod 755 /usr/local/bin/claude'`],
    { input: readFileSync(ROOT + 'test/e2e/fixtures/fake-claude.sh') })
  const audit = JSON.stringify((await api('/api/v1/changes?limit=20')).body)
  assert(audit.includes('dev.claude_auth'), 'no dev.claude_auth in the change log')
  assert(!audit.includes(token), 'token leaked into the change log: …' + audit.slice(Math.max(0, audit.indexOf(token) - 600), audit.indexOf(token) + 80))
  const envFile = sh('ssh', [HOST, `docker exec -u ${ws.user} ${C} sh -lc 'echo $CLAUDE_CODE_OAUTH_TOKEN; stat -c %a ~/.config/berth/claude.env'`])
  assert(envFile === token + '\n600', 'token in the workspace: ' + envFile)
  // a task from the drawer: queued, run, done; its diff, take it back, apply again
  await fill('任务说明', 'write hello')
  await click(btn('派出'))
  let task
  for (let i = 0; i < 60; i++) {
    task = (await api(`/api/v1/hosts/local/dev/${name}/claude`)).body.tasks[0]
    if (!['queued', 'running'].includes(task?.status)) break
    await sleep(1000)
  }
  assert(task?.status === 'done' && task.result.text === 'Wrote hello.txt.', 'task: ' + JSON.stringify(task))
  assert(task.result.files.map((f) => f.path).sort().join() === 'hello.txt,notes.md', 'files: ' + JSON.stringify(task.result.files))
  const full = (await api(`/api/v1/hosts/local/dev/${name}/claude/tasks/${task.id}`)).body
  assert(full.log.includes('working token=yes') && full.log.includes('🔧 Write hello.txt'), 'progress log: ' + full.log)
  assert(sh('ssh', [HOST, `cat ${dir}/hello.txt; stat -c %U ${dir}/hello.txt; test -e ${dir}/.git && echo has-git; true`]).startsWith('hello from claude\n'), 'file written')
  await click(`[data-task="${task.id}"] button`)
  await click(btn('看改动'))
  await waitText('hello from claude')
  await page.screenshot({ path: OUT + 'claude-task.png' })
  await click(btn('撤销改动'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  await waitText('已撤销')
  assert(sh('ssh', [HOST, `test -e ${dir}/hello.txt && echo still; test -e ${dir}/notes.md && echo still; true`]) === '', 'revert left files')
  await click(btn('重新应用改动'))
  await click('[data-testid=confirm]:not([disabled])', 15000)
  for (let i = 0; i < 20 && sh('ssh', [HOST, `cat ${dir}/hello.txt 2>/dev/null; true`]) !== 'hello from claude'; i++) await sleep(500)
  assert(sh('ssh', [HOST, `cat ${dir}/hello.txt; stat -c %u ${dir}/hello.txt`]) === 'hello from claude\n1000', 'reapply / owner')
  // the notification reached the sender's webhook
  let hook = ''
  for (let i = 0; i < 30 && !hook.includes('Claude'); i++) { await sleep(1000); hook = sh('ssh', [HOST, 'cat /tmp/e2e-hook.log 2>/dev/null; true']) }
  assert(hook.includes('Claude 完成了任务') && hook.includes(name) && hook.includes('task=' + task.id), 'notification: ' + hook + ' channels: ' + JSON.stringify((await api('/api/v1/me/channels')).body.items))
  // it asks back; the reply continues the same conversation
  assert((await change('dev.task', { name, prompt: 'ASK me' })).status === 'done', 'send ask')
  let ask
  for (let i = 0; i < 30; i++) {
    ask = (await api(`/api/v1/hosts/local/dev/${name}/claude`)).body.tasks[0]
    if (!['queued', 'running'].includes(ask.status)) break
    await sleep(1000)
  }
  assert(ask.status === 'asking' && ask.session, 'asking: ' + JSON.stringify(ask))
  const t0 = Date.now()
  const lst = await api('/api/v1/hosts/local/dev')
  const ms = Date.now() - t0
  assert(lst.body.items.find((x) => x.spec.name === name)?.claude?.asking === 1, 'list badge: ' + JSON.stringify(lst.body.items.map((x) => x.claude)))
  await page.goto(`${BASE}/local/dev`)
  await waitText('等你回答', 30000).catch(async () => { throw new Error(`no 等你回答 (list took ${ms} ms): ` + (await page.evaluate(() => document.body.innerText)).slice(0, 400)) })
  assert((await change('dev.task', { name, prompt: 'blue', reply_to: ask.id })).status === 'done', 'reply')
  let rep
  for (let i = 0; i < 30; i++) {
    rep = (await api(`/api/v1/hosts/local/dev/${name}/claude`)).body.tasks
    if (!['queued', 'running'].includes(rep[0].status)) break
    await sleep(1000)
  }
  assert(rep[0].status === 'done' && rep[0].parent === ask.id && rep[0].session === ask.session && rep[1].status === 'done', 'reply ran: ' + JSON.stringify(rep.slice(0, 2)))
  assert(sh('ssh', [HOST, `cat ${dir}/color.txt`]) === 'blue', 'reply in the same session')
  // a slow one is stopped (inside the container too); one queued behind it never starts
  assert((await change('dev.task', { name, prompt: 'SLOW' })).status === 'done', 'send slow')
  assert((await change('dev.task', { name, prompt: 'never' })).status === 'done', 'send queued')
  let tl
  for (let i = 0; i < 20; i++) {
    tl = (await api(`/api/v1/hosts/local/dev/${name}/claude`)).body.tasks
    if (tl[1].status === 'running') break
    await sleep(500)
  }
  assert(tl[1].status === 'running' && tl[0].status === 'queued', 'queue order: ' + tl.slice(0, 2).map((x) => x.status))
  assert((await change('dev.task_cancel', { name, task: tl[0].id })).status === 'done', 'cancel queued')
  assert((await change('dev.task_cancel', { name, task: tl[1].id })).status === 'done', 'cancel running')
  for (let i = 0; i < 30; i++) {
    tl = (await api(`/api/v1/hosts/local/dev/${name}/claude`)).body.tasks
    if (tl[1].status !== 'running') break
    await sleep(1000)
  }
  assert(tl[0].status === 'canceled' && tl[1].status === 'canceled', 'canceled: ' + tl.slice(0, 2).map((x) => x.status))
  assert(tl[1].before && tl[1].after && tl[1].session, 'a stopped task keeps its snapshots: ' + JSON.stringify(tl[1]))
  assert(sh('ssh', [HOST, `docker exec ${C} sh -c 'ps -eo args | grep -c "[o]utput-format stream-json"'; true`]) === '0', 'task still running in the container')
  // the resident Claude conversation in the terminal: one per workspace, opened again it comes back
  await page.goto(`${BASE}/local/dev`)
  await click(`[data-dev=${name}] ` + btn('Claude 对话'))
  await sleep(2500)
  await click(`[data-dev=${name}] ` + btn('Claude 对话'))
  await sleep(2500)
  const terms = (await api('/api/v1/hosts/local/terminals')).body.items.filter((x) => x.title === `${name} · Claude`)
  assert(terms.length === 1, 'claude terminal sessions: ' + terms.length)
  await page.screenshot({ path: OUT + 'claude-terminal.png' })
  for (const s of terms) await api(`/api/v1/hosts/local/terminals/${s.id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': await csrf() } })
  // clean up
  await api(`/api/v1/me/channels/${ch.body.id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': await csrf() } })
  sh('ssh', [HOST, 'pkill -f "python3 [/]tmp/e2e-hook.py"; rm -f /tmp/e2e-hook.py /tmp/e2e-hook.log; true'])
  assert((await change('dev.delete', { name, files: true })).status === 'done', 'delete')
  assert(sh('ssh', [HOST, `docker ps -a --format '{{.Names}}' | grep -c ${C}; test -e ${dir} && echo still-there; true`]) === '0', 'leftovers')
  // ours only (Docker keeps its own rules for other workspaces' bridges)
  assert(sh('ssh', [HOST, `(iptables -S DOCKER-USER; iptables -S INPUT) | grep -c bdev; true`]) === '0', 'guard rules left behind')
})

await spec('pve', async () => {
  // a stand-in Proxmox VE (no PVE on the dev server): self-signed HTTPS, token auth, tasks, VNC
  const secret = 'e2e-' + Math.random().toString(36).slice(2) + '-0000'
  sh('go', ['build', '-o', 'bin/linux-arm64/fakepve', './scripts/fakepve'], { env: { ...process.env, PATH: '/opt/homebrew/bin:' + process.env.PATH, GOOS: 'linux', GOARCH: 'arm64', CGO_ENABLED: '0' } })
  sh('ssh', [HOST, 'pkill -f "^/tmp/fakepve"; sleep 0.5; true'])
  sh('scp', ['-q', 'bin/linux-arm64/fakepve', HOST + ':/tmp/fakepve'])
  sh('ssh', [HOST, `(setsid nohup /tmp/fakepve -listen 127.0.0.1:18006 -secret ${secret} >/tmp/fakepve.log 2>&1 </dev/null &) ; sleep 1; head -1 /tmp/fakepve.log`])
  try {
    await page.goto(`${BASE}/local/vms`)
    await click(btn('连接 Proxmox VE'))
    await fill('PVE 地址', '127.0.0.1:18006')
    await click(btn('检查'))
    await page.waitForSelector('[data-fingerprint]', { timeout: 15000 })
    const fp = await page.$eval('[data-fingerprint]', (e) => e.textContent)
    assert(/^([0-9A-F]{2}:){31}[0-9A-F]{2}$/.test(fp), 'fingerprint: ' + fp)
    await click(text('和 PVE 里看到的一致，信任它'))
    await fill('令牌密钥', secret)
    await page.screenshot({ path: OUT + 'pve-connect.png' })
    await click('[role=dialog] ' + btn('连接'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    await page.waitForSelector('[data-pve-guest="101"]', { timeout: 20000 })
    await waitText('Proxmox VE · 127.0.0.1:18006')
    assert((await page.$$('[data-pve-node]')).length === 2, 'nodes')
    const audit = JSON.stringify((await api('/api/v1/changes?kind=pve.set')).body)
    assert(audit.includes('pve.set') && !audit.includes(secret), 'token secret in the change log')
    await page.screenshot({ path: OUT + 'pve-list.png' })
    // the container: start it, its address, a snapshot and its undo
    // the list may still be settling: click the row until its dialog opens
    for (let i = 0; i < 3; i++) {
      await clickAgain('[data-pve-guest="101"]')
      if (await page.waitForSelector('[role=dialog]', { timeout: 4000 }).catch(() => null)) break
    }
    await click('[role=dialog] ' + btn('启动'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    await waitText('10.0.9.101', 20000)
    await waitText('用 :80 建站点')
    const snap = 'e2e' + Date.now().toString(36)
    await fill('快照名', snap)
    await click(btn('拍快照'))
    await click('[data-testid=confirm]:not([disabled])', 15000)
    await page.waitForSelector(`[data-snap="${snap}"]`, { timeout: 15000 })
    await page.screenshot({ path: OUT + 'pve-drawer.png' })
    const sc = (await api('/api/v1/changes?kind=pve.snapshot')).body.items[0]
    assert((await post(`/api/v1/changes/${sc.id}/revert`, {})).status === 200, 'undo snapshot')
    await page.waitForFunction((s) => !document.querySelector(`[data-snap="${s}"]`), { timeout: 15000 }, snap)
    for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
    // the VM's console: noVNC through the panel, a real frame drawn
    await clickAgain('[data-pve-guest="100"]')
    await click('[role=dialog] ' + btn('控制台'))
    await page.waitForSelector('[data-console-state="connected"]', { timeout: 20000 })
    await page.waitForFunction(() => { const c = document.querySelector('[data-pve-console] canvas'); return c && c.width >= 640 }, { timeout: 15000 })
    await sleep(800)
    await page.screenshot({ path: OUT + 'pve-console.png' })
    for (let k = 0; k < 4 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) }
    // power with undo through the API: stop → start back
    const st = await change('pve.power', { cluster: '127-0-0-1-18006', node: 'pve1', type: 'lxc', vmid: 101, name: 'db', action: 'shutdown' })
    assert(st.status === 'done', 'shutdown: ' + JSON.stringify(st))
    const g101 = async () => (await api('/api/v1/pve')).body.items[0].guests.find((g) => g.vmid === 101).status
    assert(await g101() === 'stopped', 'stopped')
    assert((await post(`/api/v1/changes/${st.id}/revert`, {})).status === 200 && await g101() === 'running', 'undo shutdown')
    // remove: the panel forgets it
    assert((await change('pve.delete', { id: '127-0-0-1-18006' })).status === 'done', 'remove')
    assert((await api('/api/v1/pve')).body.items.length === 0, 'still listed')
  } finally {
    sh('ssh', [HOST, 'pkill -f "^/tmp/fakepve"; rm -f /tmp/fakepve /tmp/fakepve.log; true'])
  }
})

await spec('native-node', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd], { timeout: 900000 })
  const waitC = async (c) => { for (let i = 0; i < 900 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const N = 'e2e-node', SRC = '/srv/e2e-native-src', D = 'native-e2e.berth.test', SECRET = 'e2esecret' + randomBytes(8).toString('hex')
  const writeApp = (ver, buildOK = true) => onHost(`mkdir -p ${SRC} && cd ${SRC} && cat > package.json <<'X'
{"name":"e2e-native","version":"1.0.0","engines":{"node":">=20"},"scripts":{"build":"node build.js","start":"node server.js"}}
X
printf '{"name":"e2e-native","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"e2e-native","version":"1.0.0"}}}' > package-lock.json
cat > build.js <<'X'
const fs = require('fs'); fs.mkdirSync('dist', { recursive: true }); fs.writeFileSync('dist/version.txt', '${ver}')
${buildOK ? '' : "process.exit(3)"}
X
cat > worker.js <<'X'
const fs = require('fs'); setInterval(() => fs.writeFileSync('data/tick.txt', require('os').userInfo().username + ' ' + fs.readFileSync('dist/version.txt', 'utf8')), 500)
X
cat > server.js <<'X'
const fs = require('fs'), http = require('http')
const v = fs.readFileSync('dist/version.txt', 'utf8')
http.createServer((q, s) => {
  const tryw = (p) => { try { fs.writeFileSync(p, 'x'); return 'ok' } catch (e) { return 'denied' } }
  if (q.url === '/w-code') return s.end(tryw('server.js'))
  if (q.url === '/w-data') return s.end(tryw('data/probe.txt'))
  if (q.url === '/whoami') return s.end(require('os').userInfo().username)
  s.end(v)
}).listen(process.env.PORT)
X`)
  const viaSite = () => onHost(`curl -sk --max-time 5 --resolve ${D}:443:127.0.0.1 https://${D}/ || true`)
  onHost(`systemctl disable --now 'berth-proj-${N}@*' 2>/dev/null; rm -rf ${SRC} /srv/berth/projects/${N} /etc/berth/projects/${N}.env /etc/systemd/system/berth-proj-${N}@.service; userdel site-${N} 2>/dev/null; systemctl daemon-reload; true`)
  writeApp('v1')
  let site
  try {
    const set = await change('native.set', { name: N, kind: 'node', version: '22', source: { type: 'dir', path: SRC }, install: 'npm ci --no-audit --no-fund',
      build: 'npm run build', start: 'node server.js', shared: ['data'], health: '/', mem_mb: 256, env: { E2E_SECRET: SECRET }, workers: [{ name: 'tick', command: 'node worker.js' }] })
    assert(set.status === 'done', 'set: ' + JSON.stringify(set).slice(0, 300))
    const d1 = await waitC(await change('native.deploy', { name: N }))
    assert(d1.status === 'done', 'deploy v1: ' + JSON.stringify(d1.result ?? d1.error ?? d1.log?.slice(-800)))
    const port = d1.result.port
    assert(onHost(`curl -s 127.0.0.1:${port}/`) === 'v1', 'v1 answers on its port')
    assert(onHost(`curl -s 127.0.0.1:${port}/whoami`) === 'site-' + N, 'runs as its own user')
    assert(onHost(`curl -s 127.0.0.1:${port}/w-code`) === 'denied', 'cannot change its own code')
    assert(onHost(`curl -s 127.0.0.1:${port}/w-data`) === 'ok', 'can write its shared data')
    assert(!onHost(`cat /etc/systemd/system/berth-proj-${N}@.service`).includes(SECRET), 'secret not in the unit file')
    assert(onHost(`journalctl --since '-15 min' -o cat --no-pager | grep -c ${SECRET} || true`) === '0', 'secret not in the system journal')
    assert(onHost(`stat -c %a /etc/berth/projects/${N}.env`) === '600', 'env file is 0600')
    assert(onHost('nft list table inet berth_proj').includes('20000-29999'), 'outside access to project ports is blocked')
    // the worker runs beside it, as the project's user, in the current release
    let tick = ''
    for (let i = 0; i < 10 && !tick.endsWith('v1'); i++) { tick = onHost(`cat /srv/berth/projects/${N}/shared/data/tick.txt 2>/dev/null || true`); await sleep(1000) }
    assert(tick === 'site-' + N + ' v1' && onHost(`systemctl is-active berth-proj-${N}-w-tick.service`) === 'active', 'worker runs: ' + tick)
    assert(onHost('cat /etc/berth/nft/berth_proj.nft').includes('20000-29999') && onHost('systemctl is-enabled berth-nft.service') === 'enabled', 'the port guard loads at boot')
    // a site in front of it
    site = await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'project', project: N } })
    assert(site.status === 'done', 'site: ' + JSON.stringify(site.result ?? site).slice(0, 300))
    let body = ''
    for (let i = 0; i < 20 && body !== 'v1'; i++) { body = viaSite(); if (body !== 'v1') await sleep(1000) }
    assert(body === 'v1', 'the site serves v1: ' + body)
    // v2 while the site is being asked continuously: no failed request
    writeApp('v2')
    onHost(`rm -f /tmp/e2e-native-load; (for i in $(seq 1 600); do c=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 --resolve ${D}:443:127.0.0.1 https://${D}/); echo $c >> /tmp/e2e-native-load; sleep 0.1; done) >/dev/null 2>&1 & echo $! > /tmp/e2e-native-load.pid`)
    const d2 = await waitC(await change('native.deploy', { name: N }))
    assert(d2.status === 'done', 'deploy v2: ' + JSON.stringify(d2.result ?? d2.error))
    assert(d2.result.port !== port, 'v2 on the other port')
    for (let i = 0; i < 15 && viaSite() !== 'v2'; i++) await sleep(1000)
    assert(viaSite() === 'v2', 'the site serves v2')
    await sleep(3000)
    const codes = onHost('kill $(cat /tmp/e2e-native-load.pid) 2>/dev/null; sort /tmp/e2e-native-load | uniq -c')
    assert(!/\b(000|5\d\d)\b/.test(codes), 'no failed request during the switch: ' + codes)
    assert(onHost(`systemctl is-active berth-proj-${N}@${port}.service || true`) !== 'active', 'the old release was stopped')
    for (let i = 0; i < 10 && !onHost(`cat /srv/berth/projects/${N}/shared/data/tick.txt`).endsWith('v2'); i++) await sleep(1000)
    assert(onHost(`cat /srv/berth/projects/${N}/shared/data/tick.txt`).endsWith('v2'), 'the worker moved to v2 with the deploy')
    // a broken build changes nothing
    writeApp('v3', false)
    const d3 = await waitC(await change('native.deploy', { name: N }))
    assert(d3.status === 'failed', 'broken build fails')
    assert(viaSite() === 'v2', 'v2 still serves after the failed deploy')
    // the UI shows it: the list, the detail with its releases, the compatibility check
    const lst = (await api('/api/v1/hosts/local/native')).body
    const det = (await api(`/api/v1/hosts/local/native/${N}`)).body
    assert(lst.items?.length === 1 && lst.items[0].sites.length === 1 && det.sites.length === 1, 'list and detail see the project and its site: ' + JSON.stringify({ lst, sites: det.sites }).slice(0, 600))
    await page.goto(`${BASE}/local/projects/node`)
    await waitText('Git 部署', 10000).catch(() => waitText('托管部署', 10000))
    await click(`::-p-text(${N})`)
    await click('::-p-aria([name="版本"][role="radio"])')
    await waitText('正在服务', 10000)
    await page.screenshot({ path: OUT + 'native-detail.png' })
    await page.keyboard.press('Escape')
    const chk = await post('/api/v1/hosts/local/native/check', { spec: { name: 'e2e-chk', source: { type: 'dir', path: SRC } } })
    assert(chk.status === 200 && chk.body.package_manager === 'npm' && chk.body.findings.some((f) => f.code === 'node_version' || f.code === 'node_install'), 'check: ' + JSON.stringify(chk.body).slice(0, 300))
    // undo the v2 deploy: back to v1 without building
    const u = await waitC((await post(`/api/v1/changes/${d2.id}/revert`, {})).body)
    assert(u.status === 'done', 'rollback: ' + JSON.stringify(u.result ?? u.error))
    for (let i = 0; i < 15 && viaSite() !== 'v1'; i++) await sleep(1000)
    assert(viaSite() === 'v1', 'back on v1')
  } finally {
    if (site?.result?.id) await change('site.delete', { id: site.result.id })
    await change('native.delete', { name: N })
    onHost(`rm -rf ${SRC} /srv/berth/projects/${N} /tmp/e2e-native-load /tmp/e2e-native-load.pid /srv/berth/caddy/sites/${D}; userdel site-${N} 2>/dev/null; true`)
  }
})

// R1 acceptance: the real acme.uk web (Next.js 16 + Payload/SQLite + sharp) deployed natively.
// Heavy (a full Next build), so it runs only when asked for: E2E_ONLY=native-acme. The source
// must be at /srv/code/acme-web on the dev box (synced without .env, data or node_modules).
// R2 acceptance: the old PHP version of acme (~/acme) on a native PHP site, against a
// throwaway MariaDB built from its own schema. Its db.php is rewritten with test settings on the
// Mac (the real credentials never leave it). Runs only when asked: E2E_ONLY=native-legacy-php.
if (ONLY?.includes('native-legacy-php')) await spec('native-legacy-php', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd], { timeout: 900000 })
  const waitC = async (c) => { for (let i = 0; i < 1200 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const SRC = process.env.HOME + '/acme', D = 'legacy-e2e.berth.test', APP = 'legacydb', PORT = 13310
  const pw = randomBytes(12).toString('hex')
  const tmp = OUT + 'legacy-src/'
  sh('rm', ['-rf', tmp])
  sh('rsync', ['-a', '--exclude', 'CREDENTIALS.md', '--exclude', 'includes/db.php', '--exclude', 'node_modules', '--exclude', 'server', '--exclude', '.devcontainer', '--exclude', '.claude', '--exclude', '.DS_Store', SRC + '/', tmp])
  const orig = readFileSync(SRC + '/includes/db.php', 'utf8').split('\n')
  const defs = [`define('DB_HOST', '127.0.0.1;port=${PORT}');`, `define('DB_NAME', 'acme');`, `define('DB_USER', 'sftest');`, `define('DB_PASS', '${pw}');`, `define('DB_CHARSET', 'utf8mb4');`]
  writeFileSync(tmp + 'includes/db.php', [...orig.slice(0, 2), ...defs, ...orig.slice(7)].join('\n'))
  assert(!readFileSync(tmp + 'includes/db.php', 'utf8').includes(orig[5].split(',')[1] ?? '@@@'), 'the real password is not in the test copy')
  onHost(`rm -rf /srv/berth/caddy/sites/${D} /srv/berth/apps/${APP}; mkdir -p /srv/berth/caddy/sites/${D}`)
  sh('rsync', ['-a', tmp, `${HOST}:/srv/berth/caddy/sites/${D}/`])
  sh('rsync', ['-a', SRC + '/server/db/schema.sql', SRC + '/server/db/seed-content.sql', SRC + '/server/db/reseed_products.sql', `${HOST}:/tmp/`])
  let site
  try {
    const app = await waitC(await change('app.install', { app: 'mariadb', name: APP, values: { DB_PORT: String(PORT) } }))
    assert(app.status === 'done', 'mariadb')
    const c = `${APP}-mariadb-1`
    for (let i = 0; i < 30; i++) { if (onHost(`docker exec ${c} sh -c 'mariadb -uroot -p"$MARIADB_ROOT_PASSWORD" -e "select 1"' >/dev/null 2>&1 && echo y || echo n`) === 'y') break; await sleep(2000) }
    onHost(`docker exec ${c} sh -c 'mariadb -uroot -p"$MARIADB_ROOT_PASSWORD" -e "CREATE DATABASE acme CHARACTER SET utf8mb4; CREATE USER sftest@\\"%\\" IDENTIFIED BY \\"${pw}\\"; GRANT ALL ON acme.* TO sftest@\\"%\\";"'`)
    // the old site's SQL files never create testimonials (it was made by hand): the columns its queries use
    onHost(`docker exec ${c} sh -c 'mariadb -uroot -p"$MARIADB_ROOT_PASSWORD" acme -e "CREATE TABLE IF NOT EXISTS testimonials (id INT AUTO_INCREMENT PRIMARY KEY, active TINYINT DEFAULT 1, sort_order INT DEFAULT 0, name VARCHAR(200), content TEXT, updated_at DATETIME NULL) CHARACTER SET utf8mb4"'`)
    onHost(`for f in schema seed-content reseed_products; do docker exec -i ${c} sh -c 'mariadb -uroot -p"$MARIADB_ROOT_PASSWORD" acme' < /tmp/$f.sql || echo "load $f failed"; done; rm -f /tmp/schema.sql /tmp/seed-content.sql /tmp/reseed_products.sql`)
    await waitC(await change('php.native.install', { version: '8.4' }))
    site = await waitC(await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'php', php: '84', native: true, php_ini: { display_errors: 'On' } } }))
    assert(site.status === 'done', 'site: ' + JSON.stringify(site.result ?? site.error))
    const chk = (await api(`/api/v1/hosts/local/sites/${site.result.id}/php`)).body.check
    console.log('    check:', JSON.stringify({ fw: chk?.framework, v: chk?.version, f: chk?.findings?.map((x) => x.level + ':' + x.code) }))
    for (const p of ['/', '/about.php', '/travel.php', '/cloud.php', '/contact.php']) {
      const r = onHost(`curl -sk --max-time 20 -w '\\n%{http_code}' --resolve ${D}:443:127.0.0.1 https://${D}${p} || true`)
      const code = r.split('\n').pop(), body = r.slice(0, -code.length)
      console.log(`    ${p}: HTTP ${code}, ${body.length} bytes`)
      assert(code === '200' && /Acme/i.test(body) && !/Fatal error|PDOException|Warning:/.test(body), `${p} renders: ${code} ${body.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').slice(0, 300)}`)
    }
  } finally {
    if (site?.result?.id) await change('site.delete', { id: site.result.id })
    await waitC(await change('app.remove', { name: APP }))
    onHost(`rm -rf /srv/berth/caddy/sites/${D} /srv/berth/apps/${APP}; true`)
    sh('rm', ['-rf', tmp])
  }
})

// R2 acceptance: a fresh Laravel app on a native PHP site — composer in the site's folder, the
// suggested document root, a queue worker and the scheduler. E2E_ONLY=native-laravel.
if (ONLY?.includes('native-laravel')) await spec('native-laravel', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd], { timeout: 900000 })
  const waitC = async (c) => { for (let i = 0; i < 1800 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const D = 'laravel-e2e.berth.test', R = `/srv/berth/caddy/sites/${D}`
  const page = (p) => onHost(`curl -sk --max-time 20 -w '\\n%{http_code}' --resolve ${D}:443:127.0.0.1 https://${D}${p} || true`)
  onHost(`rm -rf ${R}`)
  let site
  try {
    await waitC(await change('php.native.install', { version: '8.4' }))
    site = await waitC(await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'php', php: '84', native: true } }))
    assert(site.status === 'done', 'site')
    const id = site.result.id
    const t0 = Date.now()
    const cp = await waitC(await change('site.php_run', { id, command: 'composer create-project laravel/laravel .berth-new --no-interaction --prefer-dist && cp -a .berth-new/. . && rm -rf .berth-new' }))
    console.log(`    composer create-project: ${cp.status} in ${Math.round((Date.now() - t0) / 1000)} s`)
    assert(cp.status === 'done', 'composer create-project: ' + JSON.stringify(cp.error ?? ''))
    // the check now sees Laravel and suggests public/, a queue worker and the scheduler
    const chk = (await api(`/api/v1/hosts/local/sites/${id}/php`)).body.check
    assert(chk.framework === 'laravel' && chk.root_sub === 'public' && chk.workers[0]?.command.includes('queue:work') && chk.schedule.includes('schedule:run'), 'suggestions: ' + JSON.stringify(chk).slice(0, 400))
    const s1 = (await api(`/api/v1/hosts/local/sites/${id}`)).body.site
    const ap = await waitC(await change('site.update', { ...s1, target: { ...s1.target, root_sub: chk.root_sub, php_workers: chk.workers, schedule: chk.schedule } }))
    assert(ap.status === 'done', 'apply suggestions: ' + JSON.stringify(ap.error ?? ''))
    let r = ''
    for (let i = 0; i < 15; i++) { r = page('/'); if (r.endsWith('200')) break; await sleep(1000) }
    if (!r.endsWith('200')) console.log('    laravel.log:', onHost(`grep -m3 -oE '[a-z]+\.ERROR: [^{]{0,400}' ${R}/storage/logs/laravel.log 2>/dev/null; ls -la ${R}/database | head -5; stat -c '%U %a' ${R}/storage ${R}/bootstrap/cache`))
    assert(r.endsWith('200') && /Laravel/.test(r), 'the welcome page: ' + r.slice(-300))
    // a queued job is handled by the worker; a scheduled task runs within a minute
    onHost(`cat >> ${R}/routes/console.php <<'X'

Illuminate\\Support\\Facades\\Schedule::call(fn () => file_put_contents(storage_path('app/sched.txt'), now()))->everyMinute();
X`)
    const jobPhp = Buffer.from(String.raw`<?php
namespace App\Jobs;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
class E2eJob implements ShouldQueue { use Queueable; public function handle(): void { file_put_contents(storage_path('app/queued.txt'), 'done'); } }
`.replaceAll('\\\\', '\\')).toString('base64')
    onHost(`mkdir -p ${R}/app/Jobs && echo ${jobPhp} | base64 -d > ${R}/app/Jobs/E2eJob.php`)
    const job = await waitC(await change('site.php_run', { id, command: `php artisan tinker --execute="dispatch(app()->make('App'.chr(92).'Jobs'.chr(92).'E2eJob'));" > tinker.out 2>&1` }))
    assert(job.status === 'done', 'dispatched: ' + onHost(`cat ${R}/tinker.out; echo ---; head -3 ${R}/app/Jobs/E2eJob.php`))
    let q = '', sc = ''
    for (let i = 0; i < 50; i++) {
      q = onHost(`cat ${R}/storage/app/queued.txt 2>/dev/null || true`); sc = onHost(`cat ${R}/storage/app/sched.txt 2>/dev/null || true`)
      if (q === 'done' && sc) break
      await sleep(2000)
    }
    assert(q === 'done', 'the queue worker ran the job')
    assert(sc !== '', 'the scheduler ran')
    const st = (await api(`/api/v1/hosts/local/sites/${id}/php`)).body.status
    console.log('    status:', JSON.stringify(st))
  } finally {
    if (site?.result?.id) await change('site.delete', { id: site.result.id })
    onHost(`rm -rf ${R}; true`)
  }
})

if (ONLY?.includes('native-acme')) await spec('native-acme', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd], { timeout: 900000 })
  const waitC = async (c) => { for (let i = 0; i < 3600 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const N = 'sfweb'
  const rnd = () => randomBytes(24).toString('hex')
  onHost(`systemctl disable --now 'berth-proj-${N}@*' 2>/dev/null; rm -rf /srv/berth/projects/${N} /etc/berth/projects/${N}.env /etc/systemd/system/berth-proj-${N}@.service; userdel site-${N} 2>/dev/null; systemctl daemon-reload; true`)
  const chk = await post('/api/v1/hosts/local/native/check', { spec: { name: N, source: { type: 'dir', path: '/srv/code/acme-web' } } })
  console.log('    check:', JSON.stringify({ fw: chk.body.framework, node: chk.body.node, findings: chk.body.findings?.map((f) => f.level + ':' + f.code) }))
  assert(chk.body.framework === 'next' && !chk.body.findings.some((f) => f.level === 'block'), 'check passes')
  const env = { PAYLOAD_SECRET: rnd(), BETTER_AUTH_SECRET: rnd(), CAPITAL_AUTH_SECRET: rnd(), REVALIDATE_SECRET: rnd(), DATABASE_URL: 'file:./data/payload.db',
    SITE_URL: 'https://acme.test', SKIP_TYPECHECK: '1', NEXT_BUILD_CPUS: '2', NODE_OPTIONS: '--max-old-space-size=3072' }
  try {
    const set = await change('native.set', { name: N, kind: 'node', version: '24', source: { type: 'dir', path: '/srv/code/acme-web' }, install: 'npm ci --no-audit --no-fund',
      build: 'mkdir -p data && npx next build', release: 'npx payload migrate', start: 'npx next start -p $PORT', health: '/zh-HK', shared: ['data', '.next/cache'],
      mem_mb: 1536, build_mem_mb: 6000, wait_secs: 300, env })
    assert(set.status === 'done', 'set')
    const t0 = Date.now()
    onHost('rm -f /tmp/sf-mem; (while sleep 5; do free -m | awk "/Mem:/{print \\$3}" >> /tmp/sf-mem; done) >/dev/null 2>&1 & echo $! > /tmp/sf-mem.pid')
    const d = await waitC(await change('native.deploy', { name: N }))
    const peak = onHost('kill $(cat /tmp/sf-mem.pid) 2>/dev/null; sort -n /tmp/sf-mem | tail -1')
    console.log(`    deploy: ${d.status} in ${Math.round((Date.now() - t0) / 1000)} s, host memory peak ${peak} MB`)
    if (d.status !== 'done') console.log((d.log || '').split('\n').slice(-40).join('\n'))
    assert(d.status === 'done', 'acme deployed natively')
    const code = onHost(`curl -s -o /dev/null -w '%{http_code}' --max-time 60 127.0.0.1:${d.result.port}/zh-HK`)
    assert(code === '200', '/zh-HK answers 200: ' + code)
    console.log('    serving as', onHost(`ps -o user= -p $(systemctl show -p MainPID --value berth-proj-${N}@${d.result.port}.service)`))
  } finally {
    await change('native.delete', { name: N })
    onHost(`rm -rf /srv/berth/projects/${N} /tmp/sf-mem /tmp/sf-mem.pid; userdel site-${N} 2>/dev/null; true`)
  }
})

// R4: native databases — MariaDB installed on the host (tuning with automatic rollback, the
// berth_db firewall, a container database copied over, export/import, password reset),
// PostgreSQL, and SQLite files (discovery, browsing, backed-up writes that undo).
await spec('native-db', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd], { timeout: 900000 })
  const waitC = async (c) => { for (let i = 0; i < 900 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const tcpOpen = (port) => new Promise((res) => { const s = net.connect({ host: HOST.replace(/^.*@/, ''), port, timeout: 3000 }); s.on('connect', () => { s.destroy(); res(true) }); s.on('timeout', () => { s.destroy(); res(false) }); s.on('error', () => res(false)) })
  if (onHost('ls -A /var/lib/mysql /var/lib/pgsql /var/lib/valkey 2>/dev/null | head -1') !== '') { console.log('    skipped: /var/lib/mysql or /var/lib/pgsql already holds data on this host'); return }
  const myIP = onHost('echo $SSH_CLIENT').split(' ')[0]
  const SQ = '/srv/berth/projects/e2e-sq'
  let my, pg, cont, sq, app, rd
  try {
    // ---- MariaDB ----
    const opts = (await api('/api/v1/hosts/local/databases/native')).body
    assert(opts.versions.mariadb[0] === '11.8' && opts.items.length === 0, 'install options: ' + JSON.stringify(opts).slice(0, 300))
    const t0 = Date.now()
    const ins = await waitC(await change('db.native.install', { engine: 'mariadb' }))
    console.log(`    mariadb install: ${ins.status} in ${Math.round((Date.now() - t0) / 1000)} s`)
    assert(ins.status === 'done', 'mariadb installed: ' + JSON.stringify(ins.error ?? ins.result))
    my = ins.result.id
    const list = (await api('/api/v1/hosts/local/databases')).body.items
    const inst = list.find((x) => x.id === my)
    assert(inst.native === 'mariadb 11.8' && inst.addr === '127.0.0.1:3306' && /11\.8/.test(inst.status?.version), 'registered as native: ' + JSON.stringify(inst))
    assert(onHost('grep -c "berth_admin\\|password" /etc/my.cnf.d/zz-berth.cnf || true') === '0', 'no password in the config file')
    assert(onHost(`journalctl --since '-10 min' -o cat --no-pager | grep -c '${'IDENTIFIED BY'}' || true`) === '0', 'the admin password is not in the journal')
    // databases and users work as for any instance
    assert((await change('db.create', { instance: my, name: 'e2eapp' })).status === 'done', 'create database')
    assert((await change('db.user.create', { instance: my, user: 'e2eu', name: 'e2eapp', password: 'E2eNativePw1x' })).status === 'done', 'create user')
    // a container reaches it through host.docker.internal; the outside does not
    const fromC = onHost(`docker run --rm --add-host=host.docker.internal:host-gateway -e MYSQL_PWD=E2eNativePw1x mariadb:11.4 mariadb -h host.docker.internal -u e2eu e2eapp -N -e 'select 41+1' 2>&1 || true`)
    assert(fromC === '42', 'a container connects: ' + fromC)
    assert(!(await tcpOpen(3306)), 'port 3306 is closed to the outside')
    assert(onHost('cat /etc/berth/nft/berth_db.nft').includes('tcp dport { 3306 } drop') && onHost('systemctl is-enabled berth-nft.service') === 'enabled', 'the table is loaded at boot')
    const al = await change('db.native.allow', { instance: my, sources: [myIP] })
    assert(al.status === 'done' && (await tcpOpen(3306)), 'an allowed source gets through')
    await waitC((await post(`/api/v1/changes/${al.id}/revert`, {})).body)
    assert(!(await tcpOpen(3306)), 'undo: closed again')
    // tuning: recommended values, applied and running; a value it cannot start with goes back by itself
    const tn = (await api(`/api/v1/hosts/local/databases/${my}/tune?mem=1024`)).body
    assert(tn.recommend.innodb_buffer_pool_size === '614M' && tn.host_mem_mb > 1000, 'recommendation for 1 GB: ' + JSON.stringify(tn.recommend))
    const good = await waitC(await change('db.tune', { instance: my, mem_mb: 1024, values: { ...tn.recommend, innodb_buffer_pool_size: '256M' } }))
    assert(good.status === 'done', 'tuned: ' + JSON.stringify(good.error))
    const live = (await api(`/api/v1/hosts/local/databases/${my}/tune`)).body.live
    assert(live.innodb_buffer_pool_size === '268435456' && live.max_connections === tn.recommend.max_connections, 'the new values run: ' + JSON.stringify(live))
    const bad = await waitC(await change('db.tune', { instance: my, mem_mb: 1024, values: { innodb_buffer_pool_size: '900000G' } }))
    assert(bad.status === 'failed', 'a value it cannot start with fails: ' + bad.status)
    assert(onHost('systemctl is-active mariadb') === 'active' && (await api(`/api/v1/hosts/local/databases/${my}/tune`)).body.live.innodb_buffer_pool_size === '268435456', 'the previous settings came back and it runs')
    const stats = (await api(`/api/v1/hosts/local/databases/${my}/stats`)).body
    assert(Number(stats.uptime) > 0 && Number(stats.max_connections) > 0 && stats.databases?.rows?.length > 0, 'stats: ' + JSON.stringify(stats).slice(0, 300))
    // a database in a container copied to the native server
    onHost('rm -rf /srv/berth/apps/e2ecp')
    app = await waitC(await change('app.install', { app: 'mariadb', name: 'e2ecp', values: { DB_PORT: '13307' } }))
    assert(app.status === 'done', 'container mariadb')
    const cand = (await api('/api/v1/hosts/local/databases')).body.candidates.find((x) => x.container.startsWith('e2ecp'))
    for (let i = 0; i < 30; i++) { const c = await change('db.instance.add', { name: 'e2ecp', engine: 'mariadb', container: cand.container }); if (c.status === 'done') { cont = c.result.id; break } await sleep(2000) }
    assert(cont, 'container instance')
    assert((await change('db.create', { instance: cont, name: 'legacy' })).status === 'done', 'legacy db')
    assert((await change('db.exec', { instance: cont, database: 'legacy', sql: 'CREATE TABLE posts (id INT PRIMARY KEY, title VARCHAR(50))' })).status === 'done', 'legacy table')
    assert((await change('db.exec', { instance: cont, database: 'legacy', sql: "INSERT INTO posts VALUES (1, 'hello native')" })).status === 'done', 'legacy data')
    const cp = await waitC(await change('db.copy', { from: cont, to: my, database: 'legacy' }))
    assert(cp.status === 'done', 'copied: ' + JSON.stringify(cp.error))
    const q = await post(`/api/v1/hosts/local/databases/${my}/query`, { database: 'legacy', sql: 'SELECT title FROM posts' })
    assert(q.body.rows?.[0]?.[0] === 'hello native', 'the copy holds the data: ' + JSON.stringify(q.body))
    // export and import on the native server (host tools, password in the environment only)
    const ex = await waitC(await change('db.export', { instance: my, database: 'legacy' }))
    assert(ex.status === 'done' && onHost(`test -s ${ex.result.path} && echo y`) === 'y', 'exported: ' + JSON.stringify(ex.result ?? ex.error))
    assert((await change('db.exec', { instance: my, database: 'legacy', sql: 'DELETE FROM posts' })).status === 'done', 'emptied')
    const im = await waitC(await change('db.import', { instance: my, database: 'legacy', path: ex.result.path }))
    assert(im.status === 'done', 'imported: ' + JSON.stringify(im.error))
    assert((await post(`/api/v1/hosts/local/databases/${my}/query`, { database: 'legacy', sql: 'SELECT count(*) FROM posts' })).body.rows[0][0] == 1, 'the import brought the row back')
    // the admin password reset over the local socket
    const rr = await waitC(await change('db.root_reset', { instance: my, password: 'NewAdminPw-' + randomBytes(6).toString('hex') }))
    assert(rr.status === 'done' && (await api(`/api/v1/hosts/local/databases/${my}/stats`)).status === 200, 'password reset and reconnected: ' + JSON.stringify(rr.error))
    // the page
    await page.goto(`${BASE}/local/db`)
    await waitText('127.0.0.1:3306', 10000)
    await click('::-p-text(127.0.0.1:3306)')
    await click('::-p-aria([name="概况"][role="radio"])')
    await waitText('性能调整', 10000)
    await page.screenshot({ path: OUT + 'native-db.png' })
    await page.keyboard.press('Escape')

    // ---- PostgreSQL ----
    const p0 = Date.now()
    const pins = await waitC(await change('db.native.install', { engine: 'postgres' }))
    console.log(`    postgres install: ${pins.status} in ${Math.round((Date.now() - p0) / 1000)} s`)
    assert(pins.status === 'done', 'postgres installed: ' + JSON.stringify(pins.error))
    pg = pins.result.id
    assert((await change('db.create', { instance: pg, name: 'e2epg' })).status === 'done', 'pg database')
    const pq = await post(`/api/v1/hosts/local/databases/${pg}/query`, { database: 'e2epg', sql: 'SELECT 40 + 2' })
    assert(pq.body.rows?.[0]?.[0] == 42, 'pg query: ' + JSON.stringify(pq.body))
    const ptn = await waitC(await change('db.tune', { instance: pg, mem_mb: 1024 }))
    assert(ptn.status === 'done' && (await api(`/api/v1/hosts/local/databases/${pg}/tune`)).body.live.shared_buffers === '256MB', 'pg tuned: ' + JSON.stringify(ptn.error))
    assert(onHost('cat /etc/berth/nft/berth_db.nft').includes('tcp dport { 3306, 5432 } drop'), 'both ports kept in')
    // a slow statement shows up in its slow-query list (read from the server's log)
    await post(`/api/v1/hosts/local/databases/${pg}/query`, { database: 'e2epg', sql: 'SELECT pg_sleep(2.3)' })
    let slow = []
    for (let i = 0; i < 10 && !slow.some((x) => x.sql.includes('pg_sleep')); i++) { slow = (await api(`/api/v1/hosts/local/databases/${pg}/slowlog`)).body.items ?? []; if (!slow.length) await sleep(1000) }
    assert(slow.some((x) => x.sql.includes('pg_sleep') && x.ms >= 2000), 'pg slow query listed: ' + JSON.stringify(slow).slice(0, 300))

    // ---- Redis (Valkey on Fedora) ----
    const rins = await waitC(await change('db.native.install', { engine: 'redis' }))
    assert(rins.status === 'done', 'redis installed: ' + JSON.stringify(rins.error))
    rd = rins.result.id
    const rinfo = await post(`/api/v1/hosts/local/databases/${rd}/redis`, { args: ['INFO', 'server'] })
    assert(/valkey|redis_version/i.test(JSON.stringify(rinfo.body)), 'redis answers with its password: ' + JSON.stringify(rinfo.body).slice(0, 200))
    assert(!(await tcpOpen(6379)), 'port 6379 is closed to the outside')
    assert(onHost(`grep -c requirepass /etc/valkey/berth.conf`) === '1' && onHost('stat -c %a /etc/valkey/berth.conf') === '640', 'its password is in a file only it can read')
    const rtn = await waitC(await change('db.tune', { instance: rd, mem_mb: 256 }))
    assert(rtn.status === 'done' && (await api(`/api/v1/hosts/local/databases/${rd}/tune`)).body.live.maxmemory === String(204 * 1024 * 1024), 'redis tuned: ' + JSON.stringify(rtn.error))
    const rst = (await api(`/api/v1/hosts/local/databases/${rd}/stats`)).body
    assert(Number(rst.uptime) >= 0 && rst.connections !== undefined, 'redis stats: ' + JSON.stringify(rst))
    const rrs = await waitC(await change('db.root_reset', { instance: rd, password: 'RedisNew-' + randomBytes(6).toString('hex') }))
    const after = await post(`/api/v1/hosts/local/databases/${rd}/redis`, { args: ['INFO', 'server'] })
    assert(rrs.status === 'done' && /redis_version/.test(JSON.stringify(after.body)), 'redis password changed, reconnected: ' + JSON.stringify({ s: rrs.status, e: rrs.error, a: after.body }).slice(0, 300))
    assert((await waitC(await change('db.native.remove', { instance: rd }))).status === 'done', 'redis removed')
    rd = 0

    // ---- SQLite ----
    onHost(`mkdir -p ${SQ}/shared/data && python3 -c "import sqlite3; c=sqlite3.connect('${SQ}/shared/data/app.db'); c.execute('PRAGMA journal_mode=WAL'); c.execute('CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)'); c.executemany('INSERT INTO notes(body) VALUES (?)', [('a',),('b',),('c',)]); c.commit()" && chown -R nobody: ${SQ}/shared`)
    const found = (await api('/api/v1/hosts/local/databases/sqlite/discover')).body.items.find((x) => x.path === `${SQ}/shared/data/app.db`)
    assert(found && found.project === 'e2e-sq' && found.owner === 'nobody', 'discovered with its project: ' + JSON.stringify(found))
    const sa = await change('db.instance.add', { name: 'e2e-sq-app', engine: 'sqlite', addr: `${SQ}/shared/data/app.db` })
    assert(sa.status === 'done', 'registered: ' + JSON.stringify(sa.error ?? sa))
    sq = sa.result.id
    const tb = await post(`/api/v1/hosts/local/databases/${sq}/meta`, { what: 'tables' })
    assert(tb.body.rows?.[0]?.[0] === 'notes', 'tables: ' + JSON.stringify(tb.body))
    const ro = await post(`/api/v1/hosts/local/databases/${sq}/query`, { sql: 'DELETE FROM notes' })
    assert(ro.status === 409, 'the read-only query refuses a write: ' + ro.status)
    const del = await change('db.exec', { instance: sq, sql: "DELETE FROM notes WHERE body = 'a'" })
    assert(del.status === 'done', 'write: ' + JSON.stringify(del.error))
    assert(onHost(`stat -c %U ${SQ}/shared/data/app.db ${SQ}/shared/data/app.db-wal 2>/dev/null | sort -u`) === 'nobody', 'the files still belong to the site')
    const info = (await api(`/api/v1/hosts/local/databases/${sq}/sqlite`)).body
    assert(info.tables[0].rows === 2 && info.backups.length === 1 && info.journal_mode === 'wal', 'info: ' + JSON.stringify(info).slice(0, 300))
    await waitC((await post(`/api/v1/changes/${del.id}/revert`, {})).body)
    assert((await post(`/api/v1/hosts/local/databases/${sq}/query`, { sql: 'SELECT count(*) FROM notes' })).body.rows[0][0] == 3, 'undo restored the row')
    assert(onHost(`stat -c %U ${SQ}/shared/data/app.db`) === 'nobody', 'the restored file keeps its owner')
    const chk = await change('db.sqlite.maintain', { instance: sq, action: 'check' })
    assert(chk.status === 'done' && chk.result.result === 'ok', 'integrity: ' + JSON.stringify(chk.result))
    assert((await change('db.sqlite.maintain', { instance: sq, action: 'vacuum' })).status === 'done', 'vacuum')

    // ---- removal: the service goes, the data stays ----
    const rm = await waitC(await change('db.native.remove', { instance: my }))
    assert(rm.status === 'done' && onHost('rpm -q mariadb11.8-server >/dev/null && echo y || echo n') === 'n' && onHost('test -d /var/lib/mysql/legacy && echo y') === 'y', 'removed, data kept')
    my = 0
    assert(onHost('cat /etc/berth/nft/berth_db.nft') === onHost('cat /etc/berth/nft/berth_db.nft') && !onHost('cat /etc/berth/nft/berth_db.nft').includes('3306'), 'its port left the table')
  } finally {
    const sqID = sq
    if (sq) await change('db.instance.remove', { instance: sq })
    if (cont) await change('db.instance.remove', { instance: cont })
    if (app?.status === 'done') await change('app.remove', { name: 'e2ecp' })
    if (my) await waitC(await change('db.native.remove', { instance: my }))
    if (pg) await waitC(await change('db.native.remove', { instance: pg }))
    if (rd) await waitC(await change('db.native.remove', { instance: rd }))
    onHost(`rm -rf /var/lib/mysql /var/lib/pgsql /var/lib/valkey /etc/valkey /srv/berth/apps/e2ecp ${SQ} ${sqID ? '/var/lib/berth/sqlite-backups/' + sqID : '/nonexistent'}; rm -f /srv/berth/backups/db-copy/e2ecp-legacy-* /srv/berth/backups/db-drops/mariadb-* /srv/berth/backups/db-drops/postgres-*; dnf -y -q remove mariadb11.8-server postgresql-server valkey >/dev/null 2>&1; true`)
    assert(onHost('nft list table inet berth_db >/dev/null 2>&1 && echo y || echo n') === 'n' && onHost('test -e /etc/berth/nft/berth_db.nft && echo y || echo n') === 'n', 'nothing left in the firewall')
  }
})

// System snapshots (btrfs) on the real root, without rolling back (that needs a reboot; the
// rollback itself is tested on a loop device by the agent's tests): by hand, automatically
// before a package change, deleted again; and the Migrate and Snapshots tabs render.
await spec('snapshots', async () => {
  const list = async () => (await api('/api/v1/hosts/local/system/snapshots')).body
  const before = await list()
  assert(before.supported, 'btrfs root: ' + JSON.stringify(before))
  const had = new Set(before.snapshots.map((s) => s.id))
  const made = await change('system.snapshot.create', { reason: 'e2e' })
  assert(made.status === 'done' && made.result.id, 'snapshot: ' + JSON.stringify(made).slice(0, 300))
  assert((await list()).snapshots.some((s) => s.id === made.result.id && !s.auto && s.boot), 'listed with /boot')
  const pkgWas = sh('ssh', [HOST, 'rpm -q figlet >/dev/null 2>&1 && echo 1 || echo 0'])
  let pkg
  try {
    if (pkgWas === '0') {
      pkg = await change('pkg.install', { names: ['figlet'] })
      assert(pkg.status === 'done', 'figlet: ' + JSON.stringify(pkg).slice(0, 300))
      assert((await list()).snapshots.some((s) => s.auto && s.reason === 'install figlet' && !had.has(s.id)), 'automatic one listed')
    }
    await page.goto(`${BASE}/local/system?tab=snapshots`)
    await waitText('系统快照')
    await page.goto(`${BASE}/local/system?tab=migrate`)
    await page.waitForFunction(() => location.pathname === '/local/backup/migrate', { timeout: 10000 })
    await waitText('要带走的东西', 30000)
    await click(text('迁入到这台'))
    await waitText('从哪里迁入')
  } finally {
    if (pkg?.status === 'done') await change('pkg.remove', { names: ['figlet'] })
    for (const s of (await list()).snapshots) if (!had.has(s.id)) await change('system.snapshot.delete', { id: s.id })
  }
  assert((await list()).snapshots.every((s) => had.has(s.id)), 'test snapshots removed')
})

// Server migration (R6), on the second host: everything moves from local to h2 (a Debian VM
// with a 20 GB disk), a folder too big for h2 is caught before anything is written, a sparse
// file stays sparse, a second run skips what is already there, and a migration code works.
// Opt-in (several minutes): E2E_ONLY=migrate
if (ONLY?.includes('migrate')) await spec('migrate', async () => {
  const onH2 = (cmd) => sh('ssh', [HOST, `incus exec h2 -- sh -c ${JSON.stringify(cmd)}`])
  const wasStopped = sh('ssh', [HOST, 'incus list h2 -c s -f csv']) !== 'RUNNING'
  if (wasStopped) {
    sh('ssh', [HOST, 'incus start h2'])
    for (let i = 0; i < 60; i++) { try { onH2('systemctl is-system-running --wait >/dev/null 2>&1; true'); break } catch { await sleep(2000) } }
  }
  // h2 goes back to a stock Debian: no apps, no edge, no Berth folders
  // the script goes to h2's shell on stdin: nothing in it is expanded on the way (by local's shell)
  const inH2 = (script) => sh('ssh', [HOST, 'incus exec h2 -- sh -s'], { input: script, stdio: ['pipe', 'pipe', 'inherit'] })
  const wipeH2 = () => inH2(`for p in e2e-mig berth-edge dev-e2emd; do ids=$(docker ps -aq --filter label=com.docker.compose.project=$p); [ -n "$ids" ] && docker rm -f $ids >/dev/null; done
docker rm -f berth-caddy >/dev/null 2>&1; docker network rm e2e-mig_default berth-edge >/dev/null 2>&1; docker volume rm e2e-mig_data >/dev/null 2>&1
imgs=$(docker images -q berth/caddy); [ -n "$imgs" ] && docker image rm -f $imgs >/dev/null 2>&1; docker image rm busybox:latest >/dev/null 2>&1
docker volume rm dev-e2emd_home >/dev/null 2>&1; rm -rf /srv/berth /srv/e2e-mig-share /root/e2e-mig-folder /root/e2e-mig-code; true
`)
  const wipeLocal = () => sh('ssh', [HOST, 'docker compose -p e2e-mig down -v >/dev/null 2>&1; rm -rf /srv/berth/stacks/e2e-mig /srv/berth/caddy/sites/mig-e2e.berth.test /root/e2e-mig-folder /root/e2e-mig-big /root/e2e-mig-code; true'])
  wipeLocal()
  onH2('systemctl disable --now berth-agent 2>/dev/null; rm -rf /etc/berth /var/lib/berth /usr/local/bin/berth; true')
  wipeH2()
  const jc = await post('/api/v1/hosts/join-codes', { name: 'h2' })
  onH2(jc.body.command.replaceAll('https://localhost:7092', 'https://10.42.231.1:7092'))
  let h2
  for (let i = 0; i < 40; i++) { h2 = (await api('/api/v1/hosts')).body.items.find((h) => h.name === 'h2'); if (h2?.online) break; await sleep(1000) }
  assert(h2?.online, 'h2 joined')
  const D = 'mig-e2e.berth.test'
  const hasComposeAtStart = (() => { try { onH2('docker compose version >/dev/null'); return true } catch { return false } })()
  const hasACLAtStart = onH2('command -v setfacl >/dev/null && echo 1 || echo 0') === '1'
  let site, job
  const waitLocal = async (r) => {
    if (!r.body?.id) return r.body
    for (let i = 0; i < 900; i++) { const c = (await api(`/api/v1/changes/${r.body.id}`)).body; if (!['running', 'queued', 'planned'].includes(c.status)) return c; await sleep(1000) }
    throw new Error('did not finish')
  }
  const waitH2 = async (r) => {
    const id = r.body?.id
    assert(id, 'change started: ' + JSON.stringify(r.body).slice(0, 300))
    for (let i = 0; i < 900; i++) {
      const c = (await api(`/api/v1/changes/${id}`)).body
      if (!['running', 'queued', 'planned'].includes(c.status)) return c
      await sleep(1000)
    }
    throw new Error('migration did not finish')
  }
  try {
    // what moves: a static site, a compose app with data in its volume, a job, a folder
    site = await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'static' } })
    assert(site.status === 'done', 'site: ' + JSON.stringify(site).slice(0, 300))
    sh('ssh', [HOST, `mkdir -p /srv/berth/caddy/sites/${D} && echo mig-site > /srv/berth/caddy/sites/${D}/index.html`])
    const st = await change('compose.create', { name: 'e2e-mig', compose: 'services:\n  app:\n    image: busybox:latest\n    command: ["sleep", "1d"]\n    volumes: ["data:/data"]\nvolumes:\n  data: {}\n' })
    assert(st.status === 'done', 'stack: ' + JSON.stringify(st).slice(0, 300))
    await change('compose.up', { name: 'e2e-mig' })
    sh('ssh', [HOST, 'docker exec e2e-mig-app-1 sh -c "echo mig-volume > /data/marker"'])
    job = await change('cron.set', { name: 'e2e mig job', cron: '@daily', action: { type: 'script', script: 'echo mig' } })
    // an SFTP account sharing a folder (the folder moves as a folder item), a dev workspace
    sh('ssh', [HOST, 'mkdir -p /srv/e2e-mig-share && echo shared > /srv/e2e-mig-share/a.txt'])
    const ftp = await waitLocal(await post('/api/v1/hosts/local/changes/sftp.set', { name: 'e2emigftp', dir: '/srv/e2e-mig-share', password: 'Mig-e2e-pw-123' }))
    assert(ftp.status === 'done', 'sftp: ' + JSON.stringify(ftp).slice(0, 300))
    const dev = await waitLocal(await post('/api/v1/hosts/local/changes/dev.create', { name: 'e2emd', code: false, ssh: true }))
    assert(dev.status === 'done', 'dev: ' + JSON.stringify(dev).slice(0, 400))
    sh('ssh', [HOST, 'echo dev-code > /srv/berth/dev/e2emd/code.txt && echo dev-home > $(docker volume inspect -f "{{.Mountpoint}}" dev-e2emd_home)/mark.txt'])
    sh('ssh', [HOST, 'mkdir -p /root/e2e-mig-folder /root/e2e-mig-big && echo mig-folder > /root/e2e-mig-folder/a.txt && truncate -s 2G /root/e2e-mig-folder/sparse.img && printf x | dd of=/root/e2e-mig-folder/sparse.img bs=1 seek=1000000 conv=notrunc 2>/dev/null && fallocate -l 30G /root/e2e-mig-big/fill.bin'])
    assert((await change('migrate.folders', { folders: ['/root/e2e-mig-folder', '/root/e2e-mig-big', '/srv/e2e-mig-share'] })).status === 'done', 'folders')

    // the 30 GB folder cannot fit h2's 20 GB disk: said before anything is written
    const src = { type: 'host', from: 'local' }
    let pf = await post('/api/v1/hosts/h2/migrate/preflight', { source: src })
    assert(pf.status === 200, 'preflight: ' + JSON.stringify(pf.body).slice(0, 300))
    assert(!pf.body.fits && pf.body.filesystems.some((f) => !f.ok && f.items.includes('folder:/root/e2e-mig-big')), 'too big caught: ' + JSON.stringify(pf.body.filesystems))
    // a target without docker compose (stock Debian 12): the app is marked, not attempted
    const hasCompose = (() => { try { onH2('docker compose version >/dev/null'); return true } catch { return false } })()
    if (!hasCompose) {
      assert(pf.body.checks.find((c) => c.id === 'stack:e2e-mig')?.status === 'missing', 'no compose marked: ' + JSON.stringify(pf.body.checks))
      onH2('mkdir -p /usr/local/lib/docker/cli-plugins && curl -fsSL -o /usr/local/lib/docker/cli-plugins/docker-compose https://github.com/docker/compose/releases/download/v2.29.7/docker-compose-linux-aarch64 && chmod +x /usr/local/lib/docker/cli-plugins/docker-compose')
      pf = await post('/api/v1/hosts/h2/migrate/preflight', { source: src })
    }
    const ids = pf.body.manifest.items.map((i) => i.id)
    for (const want of [`site:${site.result.id}`, 'stack:e2e-mig', 'cron', 'folder:/root/e2e-mig-folder']) assert(ids.includes(want), 'listed ' + want + ': ' + ids)
    for (const want of ['sftp', 'dev:e2emd']) assert(ids.includes(want), 'listed ' + want + ': ' + ids)
    const items = ['site:' + site.result.id, 'stack:e2e-mig', 'cron', 'folder:/root/e2e-mig-folder', 'folder:/srv/e2e-mig-share', 'sftp', 'dev:e2emd']
    pf = await post('/api/v1/hosts/h2/migrate/preflight', { source: src, items })
    assert(pf.body.fits, 'fits without it: ' + JSON.stringify(pf.body.filesystems))
    const refused = await post('/api/v1/hosts/h2/changes/migrate.import', { source: src })
    assert(refused.status === 507 || /no_room/.test(JSON.stringify(refused.body)), 'refused when it does not fit: ' + JSON.stringify(refused.body).slice(0, 300))

    // move
    const mv = await waitH2(await post('/api/v1/hosts/h2/changes/migrate.import', { source: src, items }))
    assert(mv.status === 'done', 'migrated: ' + JSON.stringify(mv).slice(0, 1500))
    assert(mv.result.done.length === items.length, 'all of them: ' + JSON.stringify(mv.result))
    // the SFTP login: same password hash, its folder mounted in its chroot
    const hashOf = (sq) => sq.split(':')[1]
    assert(hashOf(onH2('getent shadow e2emigftp')) === hashOf(sh('ssh', [HOST, 'getent shadow e2emigftp'])), 'same password hash')
    assert(onH2('cat /srv/berth/sftp/e2emigftp/files/a.txt') === 'shared', 'sftp folder mounted')
    // the workspace: its code, its home volume, running
    assert(onH2('cat /srv/berth/dev/e2emd/code.txt') === 'dev-code', 'dev code')
    assert(onH2('docker exec berth-dev-e2emd cat /home/mark.txt') === 'dev-home', 'dev home volume, running')
    assert((await api('/api/v1/hosts/h2/dev')).body.items?.some((w) => w.spec?.name === 'e2emd'), 'workspace listed on h2')
    assert(onH2(`cat /srv/berth/caddy/sites/${D}/index.html`) === 'mig-site', 'site files')
    assert(onH2('cat /root/e2e-mig-folder/a.txt') === 'mig-folder', 'folder')
    const used = Number(onH2('du -B1 -s /root/e2e-mig-folder/sparse.img').split(/\s+/)[0])
    assert(used < 50 << 20 && onH2('stat -c %s /root/e2e-mig-folder/sparse.img') === String(2 * 1024 ** 3), 'sparse stays sparse: ' + used)
    assert(onH2('docker exec e2e-mig-app-1 cat /data/marker') === 'mig-volume', 'volume data, app running')
    const h2sites = (await api('/api/v1/hosts/h2/sites')).body.items
    assert(h2sites.some((s) => s.domains.includes(D)), 'site record on h2')
    const h2jobs = (await api('/api/v1/hosts/h2/jobs')).body.items
    assert(h2jobs.filter((j) => j.name === 'e2e mig job').length === 1, 'job on h2')
    assert(onH2(`curl -sk --resolve ${D}:443:127.0.0.1 https://${D}/`) === 'mig-site', 'site served by h2')

    // again: what is there is skipped, the job is not doubled
    const again = await waitH2(await post('/api/v1/hosts/h2/changes/migrate.import', { source: src, items }))
    assert(again.status === 'done' && again.result.skipped.length === 3, 'second run skips: ' + JSON.stringify(again.result))
    assert((await api('/api/v1/hosts/h2/jobs')).body.items.filter((j) => j.name === 'e2e mig job').length === 1, 'job not doubled')

    // a migration code: sealed, pulled over HTTPS pinned to the panel's CA
    sh('ssh', [HOST, 'mkdir -p /root/e2e-mig-code && echo by-code > /root/e2e-mig-code/c.txt'])
    await change('migrate.folders', { folders: ['/root/e2e-mig-code'] })
    const code = await post('/api/v1/hosts/local/migrate/code', {})
    assert(code.status === 200 && code.body.code.startsWith('berth-migrate:'), 'code: ' + JSON.stringify(code.body))
    const bad = await post('/api/v1/hosts/h2/migrate/preflight', { source: { type: 'code' }, code: code.body.code.replace(/.{4}$/, '0000') })
    assert(bad.status >= 400 && /cert_mismatch/.test(JSON.stringify(bad.body)), 'wrong fingerprint refused: ' + JSON.stringify(bad.body))
    const byCode = await waitH2(await post('/api/v1/hosts/h2/changes/migrate.import', { source: { type: 'code' }, code: code.body.code, items: ['folder:/root/e2e-mig-code'] }))
    assert(byCode.status === 'done' && onH2('cat /root/e2e-mig-code/c.txt') === 'by-code', 'by code: ' + JSON.stringify(byCode).slice(0, 800))
    await api('/api/v1/hosts/local/migrate/code', { method: 'DELETE', headers: { 'X-CSRF-Token': await csrf() } })
  } finally {
    for (const [h, k, p] of [['h2', 'dev.delete', { name: 'e2emd' }], ['h2', 'sftp.delete', { name: 'e2emigftp' }], ['local', 'dev.delete', { name: 'e2emd' }], ['local', 'sftp.delete', { name: 'e2emigftp' }]]) {
      try { await waitLocal(await post(`/api/v1/hosts/${h}/changes/${k}`, p)) } catch { /* not there */ }
    }
    sh('ssh', [HOST, 'docker volume rm dev-e2emd_home >/dev/null 2>&1; rm -rf /srv/berth/dev/e2emd /srv/e2e-mig-share; true'])
    await change('migrate.folders', { folders: [] })
    if (job?.result?.id) await change('cron.delete', { id: job.result.id })
    if (site?.result?.id) await change('site.delete', { id: site.result.id })
    await change('compose.remove', { name: 'e2e-mig', volumes: true })
    wipeLocal()
    await change('host.remove', { name: 'h2' })
    wipeH2()
    onH2('systemctl disable --now berth-agent 2>/dev/null; rm -rf /etc/berth /var/lib/berth /usr/local/bin/berth; true')
    if (!hasComposeAtStart) onH2('rm -f /usr/local/lib/docker/cli-plugins/docker-compose')
    if (!hasACLAtStart) onH2('DEBIAN_FRONTEND=noninteractive apt-get purge -y -q acl >/dev/null 2>&1; true') // the migration installed it
    if (wasStopped) sh('ssh', [HOST, 'incus stop h2'])
  }
})

// Moving off 宝塔 (R6): h2 is dressed as a 宝塔 server (its default.db, Nginx files, sites under
// /www/wwwroot with 宝塔's .user.ini, a MariaDB with a root password on /tmp/mysql.sock) and
// moves to local: files into Berth's site folders, the database with its login and password
// into a native MariaDB, the shell job into Berth's jobs. Opt-in: E2E_ONLY=migrate-bt
if (ONLY?.includes('migrate-bt')) await spec('migrate-bt', async () => {
  const inH2 = (script) => sh('ssh', [HOST, 'incus exec h2 -- sh -s'], { input: script, stdio: ['pipe', 'pipe', 'inherit'] })
  const wasStopped = sh('ssh', [HOST, 'incus list h2 -c s -f csv']) !== 'RUNNING'
  if (wasStopped) {
    sh('ssh', [HOST, 'incus start h2'])
    for (let i = 0; i < 60; i++) { try { inH2('systemctl is-system-running --wait >/dev/null 2>&1; true'); break } catch { await sleep(2000) } }
  }
  const unBT = () => inH2(`systemctl stop mariadb >/dev/null 2>&1; DEBIAN_FRONTEND=noninteractive apt-get purge -y -q mariadb-server mariadb-client mariadb-common >/dev/null 2>&1
apt-get autoremove -y -q >/dev/null 2>&1; rm -rf /www /tmp/mysql.sock /var/lib/mysql /etc/mysql; true
`)
  const localClean = () => sh('ssh', [HOST, 'rm -rf /srv/berth/caddy/sites/btblog.e2e /srv/berth/caddy/sites/btstatic.e2e; true'])
  inH2('systemctl disable --now berth-agent 2>/dev/null; rm -rf /etc/berth /var/lib/berth /usr/local/bin/berth; true')
  unBT(); localClean()
  // the 宝塔 server
  inH2(`set -e
DEBIAN_FRONTEND=noninteractive apt-get install -y -q mariadb-server >/dev/null
mariadb -uroot <<'SQL'
CREATE DATABASE btblog; CREATE TABLE btblog.t (v VARCHAR(20)); INSERT INTO btblog.t VALUES ('hello-bt');
CREATE USER 'btblog'@'localhost' IDENTIFIED BY 'bt p''w-e2e'; GRANT ALL ON btblog.* TO 'btblog'@'localhost';
ALTER USER 'root'@'localhost' IDENTIFIED VIA mysql_native_password USING PASSWORD('btroot-e2e-pass');
SQL
ln -sf /run/mysqld/mysqld.sock /tmp/mysql.sock
mkdir -p /www/server/panel/data /www/server/panel/vhost/nginx /www/server/panel/vhost/rewrite /www/wwwroot/btblog.e2e/public /www/wwwroot/btstatic.e2e
printf '<?php echo "bt-php-" . PHP_MAJOR_VERSION;\\n' > /www/wwwroot/btblog.e2e/public/index.php
echo 'open_basedir=/www/wwwroot/btblog.e2e/:/tmp/' > /www/wwwroot/btblog.e2e/.user.ini
echo bt-static > /www/wwwroot/btstatic.e2e/index.html
printf 'server {\\n    listen 80;\\n    server_name btblog.e2e www.btblog.e2e;\\n    root /www/wwwroot/btblog.e2e/public;\\n    include enable-php-84.conf;\\n    include /www/server/panel/vhost/rewrite/btblog.e2e.conf;\\n}\\n' > /www/server/panel/vhost/nginx/btblog.e2e.conf
printf 'location / { try_files $uri $uri/ /index.php?$query_string; }\\n' > /www/server/panel/vhost/rewrite/btblog.e2e.conf
printf 'server {\\n    listen 80;\\n    root /www/wwwroot/btstatic.e2e;\\n    include enable-php-00.conf;\\n}\\n' > /www/server/panel/vhost/nginx/btstatic.e2e.conf
`)
  sh('python3', ['test/e2e/fixtures/bt-default-db.py', '/tmp/e2e-bt-default.db'])
  sh('scp', ['-q', '/tmp/e2e-bt-default.db', HOST + ':/tmp/e2e-bt-default.db'])
  rmSync('/tmp/e2e-bt-default.db', { force: true })
  sh('ssh', [HOST, 'incus file push /tmp/e2e-bt-default.db h2/www/server/panel/data/default.db && rm -f /tmp/e2e-bt-default.db'])
  const jc = await post('/api/v1/hosts/join-codes', { name: 'h2' })
  inH2(jc.body.command.replaceAll('https://localhost:7092', 'https://10.42.231.1:7092'))
  let h2
  for (let i = 0; i < 40; i++) { h2 = (await api('/api/v1/hosts')).body.items.find((h) => h.name === 'h2'); if (h2?.online) break; await sleep(1000) }
  assert(h2?.online, 'h2 joined')
  let dbInstance, nativePHPBefore
  const waitC = async (r) => {
    assert(r.body?.id, 'started: ' + JSON.stringify(r.body).slice(0, 300))
    for (let i = 0; i < 1500; i++) { const c = (await api(`/api/v1/changes/${r.body.id}`)).body; if (!['running', 'queued', 'planned'].includes(c.status)) return c; await sleep(1000) }
    throw new Error('did not finish')
  }
  try {
    nativePHPBefore = sh('ssh', [HOST, 'rpm -q php84-php-fpm >/dev/null 2>&1 && echo 1 || echo 0'])
    const src = { type: 'host', from: 'h2' }
    const pf = await post('/api/v1/hosts/local/migrate/preflight', { source: src })
    assert(pf.status === 200, 'preflight: ' + JSON.stringify(pf.body).slice(0, 400))
    const ids = pf.body.manifest.items.map((i) => i.id)
    for (const w of ['bt-site:1', 'bt-site:2', 'bt-db', 'bt-cron']) assert(ids.includes(w), 'found ' + w + ': ' + ids)
    assert(!JSON.stringify(pf.body).includes('btroot-e2e-pass'), 'the root password never leaves in the manifest')
    const blog = pf.body.manifest.items.find((i) => i.id === 'bt-site:1')
    assert(blog.info.kind === 'php' && blog.info.php === '8.4' && blog.info.notes.includes('bt.rewrite'), 'php site read: ' + JSON.stringify(blog.info))
    const items = ['bt-site:1', 'bt-site:2', 'bt-db', 'bt-cron']
    const mv = await waitC(await post('/api/v1/hosts/local/changes/migrate.import', { source: src, items }))
    assert(mv.status === 'done', 'moved: ' + JSON.stringify(mv).slice(0, 2000))
    assert(mv.result.done.length === 4, 'all four: ' + JSON.stringify(mv.result))
    assert(sh('ssh', [HOST, 'cat /srv/berth/caddy/sites/btblog.e2e/public/index.php']).includes('bt-php'), 'php files in the Berth site folder')
    assert(sh('ssh', [HOST, 'test -e /srv/berth/caddy/sites/btblog.e2e/.user.ini && echo yes || echo no']) === 'no', "宝塔's .user.ini left behind")
    assert(sh('ssh', [HOST, 'cat /srv/berth/caddy/sites/btstatic.e2e/index.html']) === 'bt-static', 'static files')
    // the app's own login, password and data, on the native MariaDB
    assert(sh('ssh', [HOST, `MYSQL_PWD="bt p'w-e2e" mariadb -ubtblog -h127.0.0.1 -N -e 'SELECT v FROM btblog.t'`]) === 'hello-bt', 'database and login')
    const sites = (await api('/api/v1/hosts/local/sites')).body.items
    const s1 = sites.find((s) => s.domains.includes('btblog.e2e'))
    assert(s1 && s1.target.type === 'php' && s1.target.native && s1.target.root_sub === 'public' && s1.domains.includes('www.btblog.e2e'), 'site: ' + JSON.stringify(s1))
    // a test domain gets no public certificate: serve it with the internal one to look at it
    assert((await change('site.update', { ...s1, tls: { mode: 'internal' } })).status === 'done', 'internal tls')
    let body = ''
    for (let i = 0; i < 20 && body !== 'bt-php-8'; i++) { body = sh('ssh', [HOST, 'curl -sk --resolve btblog.e2e:443:127.0.0.1 https://btblog.e2e/ || true']); if (body !== 'bt-php-8') await sleep(1500) }
    assert(body === 'bt-php-8', 'served by native PHP 8: ' + body)
    const jobs = (await api('/api/v1/hosts/local/jobs')).body.items
    assert(jobs.some((j) => j.name === 'bt e2e clean' && j.cron === '30 3 * * *') && !jobs.some((j) => j.name === 'bt e2e site backup'), 'jobs: ' + JSON.stringify(jobs.map((j) => j.name)))
    dbInstance = (await api('/api/v1/hosts/local/databases')).body.items?.find((d) => d.native)
  } finally {
    const tryDo = async (what, fn) => { try { await fn() } catch (e) { console.log(`    cleanup ${what}: ${e.message}`) } }
    await tryDo('sites', async () => { for (const s of (await api('/api/v1/hosts/local/sites')).body.items.filter((s) => s.domains.some((d) => d.endsWith('.e2e')))) await change('site.delete', { id: s.id }) })
    await tryDo('jobs', async () => { for (const j of (await api('/api/v1/hosts/local/jobs')).body.items.filter((j) => j.name.startsWith('bt e2e'))) await change('cron.delete', { id: j.id }) })
    await tryDo('db', async () => {
      dbInstance ??= (await api('/api/v1/hosts/local/databases')).body.items?.find((d) => d.native)
      if (dbInstance) await waitC(await post('/api/v1/hosts/local/changes/db.native.remove', { instance: dbInstance.id }))
      sh('ssh', [HOST, 'rm -rf /var/lib/mysql; nft delete table inet berth_db 2>/dev/null; rm -f /etc/berth/nft/berth_db.nft; true'])
    })
    await tryDo('php', async () => { if (nativePHPBefore === '0') { await waitC(await post('/api/v1/hosts/local/changes/php.native.remove', { version: '8.4' })); sh('ssh', [HOST, 'dnf -y -q remove "php84-*" >/dev/null 2>&1; true']) } })
    await tryDo('files', async () => localClean())
    await tryDo('host', async () => change('host.remove', { name: 'h2' }))
    await tryDo('h2', async () => { unBT(); inH2('systemctl disable --now berth-agent 2>/dev/null; rm -rf /etc/berth /var/lib/berth /usr/local/bin/berth; true') })
    if (wasStopped) sh('ssh', [HOST, 'incus stop h2'])
  }
})

// R3: Java (Spring Boot from start.spring.io, Maven), Go, FastAPI and Django deployed natively,
// each from what the compatibility check suggests. Downloads JDK/Go/Python and their packages,
// so it runs only when asked: E2E_ONLY=native-langs (or E2E_LANGS=go,fastapi to pick some).
if (ONLY?.includes('native-langs')) await spec('native-langs', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd], { timeout: 1800000 })
  const waitC = async (c) => { for (let i = 0; i < 2400 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const SRC = '/srv/e2e-r3'
  const langs = (process.env.E2E_LANGS || 'go,fastapi,django,spring').split(',')
  const want = { go: ['go', ''], fastapi: ['python', 'fastapi'], django: ['python', 'django'], spring: ['java', 'spring'] }
  onHost(`rm -rf ${SRC} && mkdir -p ${SRC}`)
  sh('rsync', ['-a', 'test/e2e/fixtures/r3/', `${HOST}:${SRC}/`])
  if (langs.includes('spring')) onHost(`cd ${SRC}/spring && curl -fsSL https://start.spring.io/starter.tgz -d dependencies=web,actuator -d type=maven-project -d javaVersion=21 -d groupId=com.example -d artifactId=demo -d packageName=com.example.demo | tar -xzf - && chmod +x mvnw`)
  for (const L of langs) {
    const N = 'e2e-' + L, dir = `${SRC}/${L}`, D = `${L}-e2e.berth.test`
    const viaSite = () => onHost(`curl -sk --max-time 5 --resolve ${D}:443:127.0.0.1 https://${D}/ || true`)
    onHost(`systemctl disable --now 'berth-proj-${N}@*' 2>/dev/null; rm -rf /srv/berth/projects/${N} /etc/berth/projects/${N}.env /etc/systemd/system/berth-proj-${N}@.service; userdel site-${N} 2>/dev/null; systemctl daemon-reload; echo v1 > ${dir}/VERSION`)
    let site
    try {
      // the check tells the language, version and commands from the project's files
      const chk = (await post('/api/v1/hosts/local/native/check', { spec: { name: N, source: { type: 'dir', path: dir } } })).body
      console.log(`    ${L} check:`, JSON.stringify({ kind: chk.kind, version: chk.version, fw: chk.framework, build: chk.build, start: chk.start, release: chk.release, findings: chk.findings?.map((f) => f.level + ':' + f.code) }))
      assert(chk.kind === want[L][0] && chk.framework === want[L][1] && !chk.findings.some((f) => f.level === 'block'), `${L}: check finds ${want[L]}`)
      const set = await change('native.set', { name: N, kind: chk.kind, version: chk.version, source: { type: 'dir', path: dir }, install: chk.install, build: chk.build,
        release: chk.release ?? '', start: chk.start, health: chk.health, shared: chk.shared, mem_mb: L === 'spring' ? 768 : 256, build_mem_mb: L === 'spring' ? 3072 : 1536,
        wait_secs: 180, env: { SECRET_KEY: randomBytes(16).toString('hex') } })
      assert(set.status === 'done', `${L}: set ` + JSON.stringify(set.error ?? ''))
      const t0 = Date.now()
      const d1 = await waitC(await change('native.deploy', { name: N }))
      console.log(`    ${L} deploy: ${d1.status} in ${Math.round((Date.now() - t0) / 1000)} s`)
      if (d1.status !== 'done') {
        const tk = ((await api('/api/v1/tasks')).body.items ?? []).find((x) => x.change_id === d1.id)
        const lines = tk ? (await api(`/api/v1/tasks/${tk.id}/logs`)).body.items ?? [] : []
        console.log(lines.slice(-40).map((l) => l.line ?? l.text ?? JSON.stringify(l)).join('\n'))
      }
      assert(d1.status === 'done', `${L}: deployed ` + JSON.stringify(d1.error ?? ''))
      const port = d1.result.port
      assert(onHost(`curl -s 127.0.0.1:${port}/`) === 'v1', `${L}: answers v1`)
      assert(onHost(`curl -s 127.0.0.1:${port}/whoami`) === 'site-' + N, `${L}: runs as its own user`)
      assert(onHost(`curl -s 127.0.0.1:${port}/w-code`) === 'denied', `${L}: cannot change its code`)
      assert(onHost(`curl -s 127.0.0.1:${port}/w-data`) === 'ok', `${L}: can write its data`)
      if (L === 'spring') assert(/"status":"UP"/.test(onHost(`curl -s 127.0.0.1:${port}/actuator/health`)), 'spring: actuator says UP')
      if (L === 'django') {
        const users = onHost(`curl -s 127.0.0.1:${port}/users`)
        if (users !== '0') console.log(onHost(`ls -la /srv/berth/projects/${N}/shared/data/; journalctl -u 'berth-proj-${N}@*' -n 30 --no-pager -o cat`), (d1.log || '').split('\n').slice(-25).join('\n'))
        assert(users === '0', 'django: migrate ran before start (the users table exists): ' + users.slice(0, 200))
        assert(onHost(`test -s /srv/berth/projects/${N}/shared/data/db.sqlite3 && echo y`) === 'y', 'django: its database is in the shared data folder')
      }
      // a second deploy behind a site: no failed request while it switches
      site = await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'project', project: N } })
      assert(site.status === 'done', `${L}: site`)
      for (let i = 0; i < 20 && viaSite() !== 'v1'; i++) await sleep(1000)
      assert(viaSite() === 'v1', `${L}: the site serves v1`)
      onHost(`echo v2 > ${dir}/VERSION; rm -f /tmp/e2e-r3-load; (for i in $(seq 1 3000); do c=$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 --resolve ${D}:443:127.0.0.1 https://${D}/); echo $c >> /tmp/e2e-r3-load; sleep 0.1; done) >/dev/null 2>&1 & echo $! > /tmp/e2e-r3-load.pid`)
      const t1 = Date.now()
      const d2 = await waitC(await change('native.deploy', { name: N }))
      console.log(`    ${L} redeploy: ${d2.status} in ${Math.round((Date.now() - t1) / 1000)} s (caches kept)`)
      assert(d2.status === 'done' && d2.result.port !== port, `${L}: v2 on the other port`)
      for (let i = 0; i < 15 && viaSite() !== 'v2'; i++) await sleep(1000)
      assert(viaSite() === 'v2', `${L}: the site serves v2`)
      await sleep(2000)
      const codes = onHost('kill $(cat /tmp/e2e-r3-load.pid) 2>/dev/null; sort /tmp/e2e-r3-load | uniq -c')
      assert(!/\b(000|5\d\d)\b/.test(codes), `${L}: no failed request during the switch: ` + codes)
      assert(onHost(`systemctl is-active berth-proj-${N}@${port}.service || true`) !== 'active', `${L}: the old release stopped`)
    } finally {
      if (site?.result?.id) await change('site.delete', { id: site.result.id })
      await change('native.delete', { name: N })
      onHost(`kill $(cat /tmp/e2e-r3-load.pid 2>/dev/null) 2>/dev/null; rm -rf /srv/berth/projects/${N} /tmp/e2e-r3-load /tmp/e2e-r3-load.pid /srv/berth/caddy/sites/${D}; userdel site-${N} 2>/dev/null; true`)
    }
  }
  onHost(`rm -rf ${SRC}`)
})

await spec('workbench', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const site = await change('site.create', { domains: ['home-e2e.berth.test'], tls: { mode: 'internal' }, target: { type: 'static' } })
  assert(site.status === 'done', 'site: ' + JSON.stringify(site.result ?? site).slice(0, 200))
  try {
    await page.setViewport({ width: 1280, height: 860 })
    await page.goto(`${BASE}/local/overview`)
    await page.waitForFunction(() => /(上午好|下午好|晚上好)/.test(document.body.innerText), { timeout: 10000 })
    await page.waitForFunction(() => /一切都在运行|件事可能需要你看一眼/.test(document.body.innerText), { timeout: 15000 })
    await waitText('home-e2e.berth.test', 10000)
    await waitText('这台服务器')
    // the grouped sidebar, current page marked
    const nav = await page.$eval('nav[aria-label="主导航"]', (n) => n.innerText)
    for (const w of ['首页', '我的东西', '网站', '保护', '服务器']) assert(nav.includes(w), 'sidebar has ' + w)
    assert(await page.$eval('nav[aria-label="主导航"] a[aria-current="page"]', (a) => a.innerText.trim()) === '首页', 'home is current')
    // the card leads to the site
    await click('::-p-text(home-e2e.berth.test)')
    await page.waitForFunction(() => location.pathname.includes('/sites/s/'), { timeout: 10000 })
    // the old dashboard now lives under 系统
    await page.goto(`${BASE}/local/system`)
    await waitText('交换', 10000)
    // narrow screens: a menu button opens the sidebar
    await page.setViewport({ width: 375, height: 812 })
    await page.goto(`${BASE}/local/overview`)
    await click(btn('菜单'))
    await page.waitForSelector('[role=dialog] nav[aria-label="主导航"]', { timeout: 5000 })
    await click('::-p-xpath(//*[@role="dialog"]//a[normalize-space(.)="网站"])')
    await page.waitForFunction(() => location.pathname.endsWith('/sites') && !document.querySelector('[role=dialog] nav'), { timeout: 5000 })
    await page.screenshot({ path: OUT + 'workbench-375.png' })
  } finally {
    await page.setViewport({ width: 1280, height: 860 })
    await change('site.delete', { id: site.result.id })
    onHost('rm -rf /srv/berth/caddy/sites/home-e2e.berth.test; true')
  }
})

await spec('r7-nav', async () => {
  // the sidebar in 1Panel/宝塔 order, with the old addresses leading to the new places
  await page.goto(`${BASE}/local/overview`)
  for (const x of ['网络', '域名', 'Cloudflare', '计划任务', '插件', '备份与还原']) await waitText(x)
  await page.goto(`${BASE}/local/system?tab=jobs`)
  await page.waitForFunction(() => location.pathname === '/local/cron', { timeout: 10000 })
  await page.goto(`${BASE}/local/domains/cloudflare`)
  await page.waitForFunction(() => location.pathname === '/local/cloudflare', { timeout: 10000 })
  await page.goto(`${BASE}/local/extensions`)
  await page.waitForFunction(() => location.pathname === '/local/plugins', { timeout: 10000 })
  await waitText('已安装的插件')
  await page.goto(`${BASE}/local/projects/runtimes`)
  await waitText('运行环境')
  await page.goto(`${BASE}/local/containers`)
  await waitText('容器')
  await page.goto(`${BASE}/local/settings/updates`)
  await waitText('版本与更新')
  await waitText('不能在线更新') // a development build carries no release key
  await page.screenshot({ path: OUT + 'r7-updates.png' })
  await page.goto(`${BASE}/local/backup/restore`)
  for (const x of ['迁移包', '面板快照', '备份仓库']) await waitText(x)
  await page.screenshot({ path: OUT + 'r7-restore.png' })
})

await spec('domains-mine', async () => {
  const D = 'mine-e2e.example.com'
  const site = await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'static' } })
  assert(site.status === 'done', 'site: ' + JSON.stringify(site.error ?? site.result))
  try {
    const ov = (await api('/api/v1/hosts/local/domains/overview')).body
    const root = ov.items.find((r) => r.root === 'example.com')
    const sub = root?.subs.find((x) => x.name === D)
    assert(sub && sub.used_by.some((u) => u.type === 'site') && ['none', 'elsewhere'].includes(sub.points), 'overview: ' + JSON.stringify(root))
    await page.goto(`${BASE}/local/domains`)
    await waitText('example.com', 20000)
    await waitText(D)
    await page.screenshot({ path: OUT + 'domains-mine.png' })
  } finally {
    await change('site.delete', { id: site.result.id })
    sh('ssh', [HOST, `rm -rf /srv/berth/caddy/sites/${D}; true`])
  }
})

await spec('ai-activity', async () => {
  const created = await post('/api/v1/tokens', { kind: 'agt', name: 'e2e-claude', scopes: ['overview:read', 'containers:read'], danger_policy: 'run' })
  assert(created.status === 200 && created.body.token, 'token: ' + JSON.stringify(created.body))
  const tok = created.body.token, id = created.body.item.id
  try {
    const mcp = (method, params) => page.evaluate(async (t, m, p) => (await fetch('/mcp', { method: 'POST', headers: { Authorization: 'Bearer ' + t, 'Content-Type': 'application/json' },
      body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: m, params: p }) })).json(), tok, method, params)
    await mcp('tools/call', { name: 'list_containers', arguments: {} })
    const r = await mcp('tools/call', { name: 'get_container', arguments: { name: CONTAINER } })
    assert(r.result && !r.result.isError, 'mcp call: ' + JSON.stringify(r).slice(0, 300))
    const act = (await api('/api/v1/tokens/activity')).body.items.find((x) => x.name === 'e2e-claude')
    assert(act && act.working && act.count_24h >= 2 && act.recent[0].method === 'get_container' && act.recent[0].obj_id === CONTAINER, 'activity: ' + JSON.stringify(act))
    await page.goto(`${BASE}/local/overview`)
    await waitText('AI 正在做什么')
    await waitText('e2e-claude')
    await page.screenshot({ path: OUT + 'ai-home.png' })
    await page.goto(`${BASE}/local/containers/c/${CONTAINER}`)
    await waitText('e2e-claude 正在操作这里', 15000)
    await page.screenshot({ path: OUT + 'ai-object.png' })
    await page.goto(`${BASE}/local/tokens`)
    await waitText('正在工作')
  } finally {
    await api(`/api/v1/tokens/${id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': await csrf() } })
  }
})

await spec('restore-online', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  // a panel snapshot made here, uploaded back in pieces, checked, then restored online:
  // a setting changed after the snapshot comes back to what it was
  await post('/api/v1/me/reauth', { password: PASSWORD })
  const before = await change('settings.update', { values: { 'panel.name': 'e2e-before' } })
  assert(before.status === 'done', 'name set')
  const snapPw = 'e2e-snap-' + Math.random().toString(36).slice(2)
  const up = await page.evaluate(async (pw) => {
    const csrf = (await fetch('/api/v1/me').then((r) => r.json())).csrf
    const h = { 'X-CSRF-Token': csrf, 'Content-Type': 'application/json' }
    const blob = await (await fetch('/api/v1/panel/snapshot', { method: 'POST', headers: h, body: JSON.stringify({ password: pw }) })).blob()
    let u = await (await fetch('/api/v1/uploads', { method: 'POST', headers: h, body: JSON.stringify({ name: 'e2e.berthsnap', size: blob.size }) })).json()
    const half = Math.floor(blob.size / 2)
    for (const [a, b] of [[0, half], [half, blob.size]]) u = await (await fetch(`/api/v1/uploads/${u.id}?offset=${a}`, { method: 'PUT', headers: { 'X-CSRF-Token': csrf }, body: blob.slice(a, b) })).json()
    return u
  }, snapPw)
  assert(up.done, 'uploaded: ' + JSON.stringify(up))
  const bad = await post('/api/v1/panel/restore/check', { upload: up.id, password: 'wrong-password-1' })
  assert(bad.status >= 400, 'wrong password refused')
  const ok = await post('/api/v1/panel/restore/check', { upload: up.id, password: snapPw })
  assert(ok.status === 200 && ok.body.detail.includes('snapshot ok') && ok.body.units.join() === 'berth-panel-e2e', 'checked: ' + JSON.stringify(ok.body))
  await change('settings.update', { values: { 'panel.name': 'e2e-after' } })
  const r = await change('panel.restore', { upload: up.id, password: snapPw })
  assert(r.status === 'done', 'restore started: ' + JSON.stringify(r.error ?? r.result))
  // the panel goes away and comes back
  let back = false
  await sleep(4000)
  for (let i = 0; i < 60 && !back; i++) {
    await sleep(1500)
    back = await page.evaluate(async () => { try { return (await fetch('/api/v1/me')).status < 500 } catch { return false } }).catch(() => false)
  }
  assert(back, 'the panel came back')
  if ((await api('/api/v1/me')).status === 401) await page.goto(`${BASE}/login`) // signed out by the swap: sign in again
  const st = (await api('/api/v1/settings')).body
  assert(st?.values?.['panel.name'] === 'e2e-before', 'the snapshot\'s setting is back: ' + JSON.stringify(st?.values?.['panel.name']))
  const kept = onHost('ls -d /var/lib/berth-e2e.before-restore-* /etc/berth-e2e.before-restore-* 2>/dev/null | wc -l')
  assert(kept === '2', 'old folders kept: ' + kept)
  onHost('rm -rf /var/lib/berth-e2e.before-restore-* /etc/berth-e2e.before-restore-*; systemctl is-active berth-panel-e2e')
})

await spec('plugins', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const work = OUT + 'plugins/'
  rmSync(work, { recursive: true, force: true })
  mkdirSync(work, { recursive: true })
  const berth = (...a) => sh('go', ['run', './cmd/berth', 'plugin', ...a])
  const hadIperf = onHost('rpm -q iperf3 >/dev/null 2>&1 && echo y || echo n') === 'y'
  sh('make', ['-s'], { cwd: ROOT + 'examples/plugins/iperf3' })
  sh('make', ['-s'], { cwd: ROOT + 'examples/plugins/whoami-root' })
  berth('keygen', work + 'k.key')
  berth('pack', 'examples/plugins/iperf3', '--key', work + 'k.key', '-o', work + 'iperf3.berthplugin')
  berth('pack', 'examples/plugins/whoami-root', '-o', work + 'root-unsigned.berthplugin')
  berth('pack', 'examples/plugins/whoami-root', '--key', work + 'k.key', '-o', work + 'root.berthplugin')
  const uploadVia = async (file) => {
    await page.goto(`${BASE}/local/plugins`)
    await click(btn('手动安装'))
    const input = await page.waitForSelector('[role=dialog] input[type=file]')
    await input.uploadFile(file)
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"][not(@disabled)])', 30000)
  }
  try {
    // iperf3: signed, runs as its own user, Berth installs the iperf3 package for it
    await uploadVia(work + 'iperf3.berthplugin')
    await waitText('网速测试（iperf3）', 15000)
    await waitText('开发者签了名')
    await page.screenshot({ path: OUT + 'plugin-intro.png' })
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"])')
    await waitText('它在这台服务器上需要什么')
    await waitText('先安装系统软件包：iperf3')
    await page.screenshot({ path: OUT + 'plugin-perms.png' })
    await click(text('我知道它会在这台服务器上做上面这些事'))
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"][not(@disabled)])')
    await waitText('服务端端口')
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"])')
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="安装"])')
    await click('[data-testid=confirm]:not([disabled])', 30000)
    await waitText('插件装好了', 240000)
    assert((await api('/api/v1/plugins')).body.items.some((x) => x.id === 'iperf3'), 'recorded as installed')
    assert(onHost('systemctl is-active berth-plugin-iperf3; true') === 'active', 'service running')
    assert(onHost('systemctl show berth-plugin-iperf3 -p User --value') === 'berth-p-iperf3', 'its own user')
    assert(onHost('command -v iperf3 >/dev/null && echo y') === 'y', 'iperf3 installed')
    const closeDrawers = async () => { for (let k = 0; k < 3 && (await page.$$('[role=dialog]')).length > 0; k++) { await page.keyboard.press('Escape'); await sleep(400) } }
    await closeDrawers() // the task log drawer covers the page
    await click(btn('打开'))
    const frame = await (await page.waitForSelector('iframe', { timeout: 15000 })).contentFrame()
    await frame.waitForFunction(() => document.body.innerText.includes('iperf3 server'), { timeout: 15000 })
    await page.screenshot({ path: OUT + 'plugin-page.png' })
    const list = (await api('/api/v1/plugins')).body.items
    const p = list.find((x) => x.id === 'iperf3')
    assert(p && p.kind === 'native' && p.running && p.fingerprint, 'listed: ' + JSON.stringify(p))

    // an unsigned root plugin is refused; the signed one asks for the id to be typed
    await uploadVia(work + 'root-unsigned.berthplugin')
    await waitText('以 root 运行的插件必须有签名', 15000)
    await closeDrawers()
    await uploadVia(work + 'root.berthplugin')
    await waitText('开发者签了名', 15000)
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"])')
    await waitText('这个插件将以 root 运行')
    await page.screenshot({ path: OUT + 'plugin-root.png' })
    await click(text('我完全信任这个插件的作者，同意它以 root 运行'))
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="继续"][not(@disabled)])')
    await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="安装"])')
    await fill('输入 whoami-root 以确认', 'whoami-root')
    await click('[data-testid=confirm]:not([disabled])', 30000)
    await waitText('插件装好了', 120000)
    await closeDrawers()
    assert(onHost('systemctl show berth-plugin-whoami-root -p User --value') === 'root', 'runs as root')
  } finally {
    for (const id of ['iperf3', 'whoami-root']) await change('plugin.remove', { id }).catch(() => {})
    // leave the machine as it was: the iperf3 package the plugin asked for, and the shared plugin folders
    onHost(`${hadIperf ? 'true' : 'dnf -y -q remove iperf3 >/dev/null 2>&1'}; rm -f /var/lib/berth-plugins/berth-ca.crt; rmdir /var/lib/berth-plugins /etc/berth-plugins 2>/dev/null; semanage fcontext -d '/var/lib/berth-plugins/[^/]+/app(/.*)?' 2>/dev/null; true`)
  }
  const left = onHost('ls /etc/systemd/system | grep -c berth-plugin; id berth-p-iperf3 >/dev/null 2>&1 && echo user-left; true')
  assert(left.startsWith('0') && !left.includes('user-left'), 'removed cleanly: ' + left)
})

await spec('vm-network', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const N = 'e2e-net'
  const waitC = async (c) => { for (let i = 0; i < 600 && ['running', 'queued'].includes(c.status); i++) { await sleep(1000); c = (await api(`/api/v1/changes/${c.id}`)).body } return c }
  const inside = (cmd) => onHost(`incus exec ${N} -- sh -c '${cmd}' 2>&1; true`)
  onHost(`incus delete -f ${N} 2>/dev/null; incus network acl delete berth-${N} 2>/dev/null; rm -rf /srv/e2e-share; mkdir -p /srv/e2e-share && echo shared-ok > /srv/e2e-share/hello.txt; true`)
  try {
    const c = await waitC(await change('vm.create', { name: N, image: 'debian/13/cloud', vm: false, size: 'small' }))
    assert(c.status === 'done', 'create: ' + JSON.stringify(c.error ?? c.result))
    const ip = onHost(`for i in $(seq 1 60); do a=$(incus list ${N} -c 4 -f csv | cut -d' ' -f1); [ -n "$a" ] && echo $a && break; sleep 2; done`)
    assert(/^10\./.test(ip), 'NAT address: ' + ip)
    // a forward, then outbound limited to the LAN: the internet is out of reach, the LAN is not
    assert((await waitC(await change('vm.forward', { name: N, proto: 'tcp', host_port: 22999, target_port: 22 }))).status === 'done', 'forward')
    assert((await waitC(await change('vm.network', { name: N, mode: 'nat', egress: 'lan' }))).status === 'done', 'egress lan')
    for (let i = 0; i < 20 && !inside('ip -4 -br addr show eth0').includes('10.'); i++) await sleep(2000)
    assert(onHost(`incus network acl show berth-${N} | grep -c 192.168.0.0/16`) === '1', 'acl written')
    const lanGw = onHost("ip route | awk '/default/ {print $3; exit}'")
    assert(inside(`ping -c1 -W3 ${lanGw} >/dev/null && echo lan-ok`).includes('lan-ok'), 'LAN reachable')
    assert(!inside('ping -c1 -W3 1.1.1.1 >/dev/null && echo net-ok').includes('net-ok'), 'internet blocked')
    const none = await waitC(await change('vm.network', { name: N, mode: 'nat', egress: 'none' }))
    assert(none.status === 'done', 'egress none')
    for (let i = 0; i < 20 && !inside('ip -4 -br addr show eth0').includes('10.'); i++) await sleep(2000)
    assert(!inside(`ping -c1 -W3 ${lanGw} >/dev/null && echo lan-ok`).includes('lan-ok'), 'LAN blocked too')
    // its own address on the LAN from the router; the forward goes, and comes back on undo
    const port = onHost("ip route | awk '/default/ {print $5; exit}'")
    const lan = await waitC(await change('vm.network', { name: N, mode: 'lan', parent: port }))
    assert(lan.status === 'done' && lan.result.removed_forwards.length === 1, 'to LAN: ' + JSON.stringify(lan.result ?? lan.error))
    let lanIP = ''
    for (let i = 0; i < 30 && !lanIP; i++) { lanIP = (/inet (\d+\.\d+\.\d+\.\d+)/.exec(inside('ip -4 -o addr show eth0')) ?? [])[1] ?? ''; if (!lanIP || lanIP.startsWith('10.4')) { lanIP = ''; await sleep(2000) } }
    const hostNet = onHost(`ip -4 -br addr show ${port} | awk '{print $3}'`)
    assert(lanIP && hostNet.split('.').slice(0, 2).join('.') === lanIP.split('.').slice(0, 2).join('.'), `LAN address ${lanIP} on ${hostNet}`)
    await page.goto(`${BASE}/local/vms`)
    await click(`::-p-xpath(//div[contains(@class,"font-semibold")][starts-with(normalize-space(.),"${N} ")])`)
    await waitText('局域网独立 IP（桥接）')
    await waitText(lanIP)
    await page.screenshot({ path: OUT + 'vm-network.png' })
    await page.keyboard.press('Escape')
    const back = await waitC((await post(`/api/v1/changes/${lan.id}/revert`, {})).body)
    assert(back.status === 'done', 'undo: ' + JSON.stringify(back.error ?? back.result))
    const inst = (await api(`/api/v1/hosts/local/vms`)).body.items.find((x) => x.name === N)
    assert(inst.net.mode === 'nat' && inst.forwards.some((f) => f.host_port === 22999), 'back in NAT with its forward: ' + JSON.stringify({ net: inst.net, f: inst.forwards }))
    // permissions and a read-only shared folder
    assert((await waitC(await change('vm.perms', { name: N, autostart: true, nesting: true, privileged: false }))).status === 'done', 'perms')
    assert(onHost(`incus config get ${N} security.nesting; incus config get ${N} boot.autostart`) === 'true\ntrue', 'perms applied')
    assert((await waitC(await change('vm.share', { name: N, id: 'e2e', source: '/srv/e2e-share', path: '/mnt/e2e', readonly: true }))).status === 'done', 'share')
    assert(inside('cat /mnt/e2e/hello.txt').includes('shared-ok'), 'shared file seen inside')
    assert(!inside('touch /mnt/e2e/x && echo wrote').includes('wrote'), 'read-only inside')
  } finally {
    onHost(`incus delete -f ${N} 2>/dev/null; incus network acl delete berth-${N} 2>/dev/null; rm -rf /srv/e2e-share; true`)
  }
})

await spec('snippets', async () => {
  await page.goto(`${BASE}/local/terminal`)
  await click(btn('在 local 上'))
  await page.waitForSelector('.xterm-helper-textarea', { timeout: 10000 })
  await click(btn('命令库'))
  await click(text('常用推荐'))
  await click(btn('收藏到我的 内存'))
  await click(text('我的 1'))
  await click(btn('执行 内存'))
  await page.waitForFunction(() => document.querySelector('.xterm-rows')?.textContent.includes('Mem:'), { timeout: 10000 })
  // a command with a part to fill in: asked for, then run
  await click(btn('新建'))
  await fill('名称', 'e2e 回显')
  await fill('命令', 'echo e2e-{{词:hello}}-ok')
  await waitText('使用时会问')
  await click('::-p-xpath(//*[@role="dialog"]//button[normalize-space(.)="保存"])')
  await click(btn('执行 e2e 回显'))
  await fill('词', 'snip')
  await click('::-p-xpath(//*[@role="dialog"]//button[contains(normalize-space(.),"执行")])')
  await page.waitForFunction(() => document.querySelector('.xterm-rows')?.textContent.includes('e2e-snip-ok'), { timeout: 10000 })
  await page.screenshot({ path: OUT + 'snippets.png' })
  const list = (await api('/api/v1/snippets')).body.items
  assert(list.length === 2 && list.some((x) => x.name === 'e2e 回显' && x.uses === 1), 'saved and counted: ' + JSON.stringify(list))
  for (const x of list) await api(`/api/v1/snippets/${x.id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': await csrf() } })
  const sessions = await api('/api/v1/hosts/local/terminals')
  for (const s of sessions.body.items) await page.evaluate(async (id) => fetch(`/api/v1/hosts/local/terminals/${id}`, { method: 'DELETE', headers: { 'X-CSRF-Token': (await fetch('/api/v1/me').then((r) => r.json())).csrf } }), s.id)
})

await spec('error-pages', async () => {
  const onHost = (cmd) => sh('ssh', [HOST, cmd])
  const D = 'errors-e2e.berth.test'
  const get = (path) => onHost(`curl -sk --max-time 10 -w '\\n%{http_code}' --resolve ${D}:443:127.0.0.1 https://${D}${path} || true`)
  const site = await change('site.create', { domains: [D], tls: { mode: 'internal' }, target: { type: 'static' } })
  assert(site.status === 'done', 'site: ' + JSON.stringify(site.error ?? site.result))
  try {
    let r = ''
    for (let i = 0; i < 20 && !r.endsWith('404'); i++) { r = get('/no-such-page'); if (!r.endsWith('404')) await sleep(1000) }
    assert(r.endsWith('404') && r.includes('页面不存在') && r.includes('Page not found') && !r.includes('{http.request.uuid}') && r.includes('<style>*{box-sizing'), 'not found page: ' + r.slice(-400))
    // only addresses from elsewhere may come in: this machine is blocked, and sees the blocked page
    const s = (await api('/api/v1/hosts/local/sites')).body.items.find((x) => x.id === site.result.id)
    assert((await change('site.update', { ...s, options: { ...s.options, allow_ips: ['192.0.2.0/24'] } })).status === 'done', 'allow list')
    for (let i = 0; i < 20 && !r.endsWith('403'); i++) { r = get('/'); if (!r.endsWith('403')) await sleep(1000) }
    assert(r.endsWith('403') && r.includes('访问被拦截') && r.includes('Access blocked'), 'blocked page: ' + r.slice(-300))
    writeFileSync(OUT + 'error-403.html', r.replace(/\n\d+$/, ''))
    // a stopped site says it is paused (and still has its certificate)
    assert((await change('site.toggle', { id: site.result.id, enabled: false })).status === 'done', 'stopped')
    for (let i = 0; i < 20 && !r.endsWith('503'); i++) { r = get('/'); if (!r.endsWith('503')) await sleep(1000) }
    assert(r.endsWith('503') && r.includes('网站暂停访问'), 'stopped page: ' + r.slice(-300))
      const shot = await browser.newPage() // the page as a visitor sees it, in its own tab
    await shot.setContent(r.replace(/\n\d+$/, ''))
    await shot.screenshot({ path: OUT + 'error-stopped.png' })
    await shot.close()
  } finally {
    await change('site.delete', { id: site.result.id })
    onHost(`rm -rf /srv/berth/caddy/sites/${D}; true`)
  }
  // the panel's own pages: a missing one
  await page.goto(`${BASE}/local/no-such-page`)
  await waitText('这个页面不存在')
  await page.screenshot({ path: OUT + 'panel-404.png' })
})

await spec('screens', async () => {
  for (const theme of ['light', 'dark']) {
    for (const [w, h] of [[1280, 860], [375, 812]]) {
      await page.setViewport({ width: w, height: h })
      for (const p of ['overview', 'sites', 'containers', 'audit', 'tokens', 'store', 'db', 'backup', 'monitor', 'network', 'vms', 'remote', 'system', 'cron', 'system?tab=access', 'system?tab=kernel', 'system?tab=disks', 'plugins', 'domains', 'cloudflare', 'projects', 'backup/restore']) {
        await page.evaluate((t) => localStorage.setItem('berth.theme', t), theme)
        await page.goto(`${BASE}/local/${p}`)
        await sleep(1500)
        await page.screenshot({ path: `${OUT}${p.replace(/[/?=]/g, '_')}-${theme}-${w}.png` })
      }
    }
  }
  await page.setViewport({ width: 1280, height: 860 })
})

await browser.close()
tunnel.kill()
if (madeSmoke) try { sh('ssh', [HOST, `cd /srv/e2e-smoke 2>/dev/null && docker compose -p ${PROJECT} down >/dev/null 2>&1; rm -rf /srv/e2e-smoke; true`]) } catch { /* best effort */ }
// the test panel shared the real edge: the main panel puts its own sites back when it starts
try { sh('ssh', [HOST, 'systemctl stop berth-panel-e2e; systemctl restart berth-panel; rm -rf /var/lib/berth-e2e /etc/berth-e2e /var/lib/berth-e2e.before-restore-* /etc/berth-e2e.before-restore-* /usr/local/bin/berth-panel-e2e']) } catch { /* best effort */ }
writeFileSync(OUT + 'results.json', JSON.stringify(results, null, 2))
const failed = results.filter((r) => r[1] !== 'ok')
console.log(`\n${results.length - failed.length}/${results.length} passed; screenshots in test/e2e/out/`)
process.exit(failed.length ? 1 : 0)
