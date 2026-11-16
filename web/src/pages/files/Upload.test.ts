import { describe, expect, it, vi } from 'vitest'

const calls: string[] = []
let failSecondOnce = true
let serverReceived = 0
vi.mock('@/lib/api', async (orig) => ({
  ...(await orig<typeof import('@/lib/api')>()),
  post: vi.fn(async (p: string) => { calls.push('POST ' + p); return p.endsWith('/finish') ? { status: 'done' } : { id: 'u1' } }),
  get: vi.fn(async (p: string) => { calls.push('GET ' + p); return { received: serverReceived } }),
  api: vi.fn(async (_m: string, p: string, body: Blob) => {
    calls.push('PUT ' + p)
    const off = Number(new URL(p, 'http://x').searchParams.get('offset'))
    if (off === 8 << 20 && failSecondOnce) { failSecondOnce = false; throw new Error('net') }
    serverReceived = off + body.size
    return { received: serverReceived }
  }),
}))
import { uploadFile, CHUNK } from './Upload'

describe('uploadFile', () => {
  it('sends 8 MB chunks, retries a failed one from the server offset, then finishes', async () => {
    const file = new Blob([new Uint8Array(20 << 20)])
    await uploadFile({ host: 'local', file, dest: '/srv/berth/big.bin' })
    const puts = calls.filter((c) => c.startsWith('PUT')).map((c) => Number(new URL(c.slice(4), 'http://x').searchParams.get('offset')))
    expect(puts).toEqual([0, CHUNK, CHUNK, 2 * CHUNK])
    expect(calls.some((c) => c.startsWith('GET'))).toBe(true)
    expect(calls[calls.length - 1]).toBe('POST /api/v1/hosts/local/files/upload/u1/finish')
  })
})
