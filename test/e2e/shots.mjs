// Screenshots of pages on an already running e2e panel (https://localhost:7092, tunnelled).
//   node shots.mjs <user> <password> path[|text to click] ...   → out/shot-N.png
import { existsSync, mkdirSync } from 'node:fs'
import { setTimeout as sleep } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const [user, pw, ...paths] = process.argv.slice(2)
const OUT = fileURLToPath(new URL('./out/', import.meta.url))
mkdirSync(OUT, { recursive: true })
const browserPath = [process.env.CHROME_PATH, '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge'].find((p) => p && existsSync(p))
const browser = await puppeteer.launch({ executablePath: browserPath, headless: true, acceptInsecureCerts: true, args: ['--ignore-certificate-errors', '--no-first-run'] })
const page = await browser.newPage()
await page.setViewport({ width: 1360, height: 900, deviceScaleFactor: 1 })
const errors = []
page.on('pageerror', (e) => errors.push(e.message))
page.on('console', (m) => m.type() === 'error' && errors.push(m.text()))
await page.goto('https://localhost:7092/login')
await page.waitForSelector('input')
const inputs = await page.$$('input')
await inputs[0].type(user)
await inputs[1].type(pw)
await page.keyboard.press('Enter')
await sleep(1500)
for (const [i, p] of paths.entries()) {
  const [path, ...clicks] = p.split('|')
  await page.goto('https://localhost:7092' + path)
  await sleep(1800)
  for (const click of clicks) {
    const el = await page.$(`::-p-text(${click})`)
    if (el) {
      await el.click()
      await sleep(3000)
    } else errors.push('no element: ' + click)
  }
  await page.screenshot({ path: `${OUT}shot-${i}.png` })
  console.log(`shot-${i}.png ${p}`)
}
console.log(errors.length ? 'ERRORS:\n' + errors.join('\n') : 'no page errors')
await browser.close()
