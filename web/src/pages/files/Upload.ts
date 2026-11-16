import { api, get, post } from '@/lib/api'

export const CHUNK = 8 << 20
const RETRIES = 3

export interface UploadOpts {
  host: string
  file: Blob
  dest: string // absolute path of the new file
  onProgress?: (sent: number, total: number) => void
  signal?: AbortSignal
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

// uploadFile sends a file in 8 MB chunks; a failed chunk is retried, and after an error
// the upload resumes from what the server already has.
export async function uploadFile({ host, file, dest, onProgress, signal }: UploadOpts) {
  const base = `/api/v1/hosts/${encodeURIComponent(host)}/files/upload`
  const { id } = await post<{ id: string }>(base, { path: dest, size: file.size })
  let offset = 0
  let failures = 0
  while (offset < file.size) {
    if (signal?.aborted) throw new DOMException('aborted', 'AbortError')
    const chunk = file.slice(offset, Math.min(offset + CHUNK, file.size))
    try {
      const r = await api<{ received: number }>('PUT', `${base}/${id}?offset=${offset}`, chunk)
      offset = r.received
      failures = 0
      onProgress?.(offset, file.size)
    } catch (e) {
      if (++failures > RETRIES) throw e
      await sleep(500 * failures)
      // ask where the server is, then continue from there
      try { offset = (await get<{ received: number }>(`${base}/${id}`)).received } catch { /* keep offset */ }
    }
  }
  return post(`${base}/${id}/finish`, { path: dest })
}

// runQueue uploads files two at a time.
export async function runQueue<T>(items: T[], worker: (x: T) => Promise<unknown>, parallel = 2) {
  const queue = [...items]
  const errors: unknown[] = []
  await Promise.all(Array.from({ length: Math.min(parallel, queue.length) }, async () => {
    for (let x = queue.shift(); x !== undefined; x = queue.shift()) {
      try { await worker(x) } catch (e) { errors.push(e) }
    }
  }))
  return errors
}
