// Web Push: register the service worker and subscribe this browser with the panel's VAPID key.
import { post } from './api'

export const pushSupported = () => typeof window !== 'undefined' && 'serviceWorker' in navigator && 'PushManager' in window && window.isSecureContext

function b64url(s: string): Uint8Array {
  const pad = '='.repeat((4 - (s.length % 4)) % 4)
  const raw = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'))
  return Uint8Array.from(raw, (c) => c.charCodeAt(0))
}

export async function currentSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null
  const reg = await navigator.serviceWorker.getRegistration('/')
  return reg ? reg.pushManager.getSubscription() : null
}

export async function subscribePush(vapid: string, name: string): Promise<number> {
  const perm = await Notification.requestPermission()
  if (perm !== 'granted') throw new Error('denied')
  const reg = await navigator.serviceWorker.register('/sw.js', { scope: '/' })
  await navigator.serviceWorker.ready
  let sub = await reg.pushManager.getSubscription()
  if (!sub) sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64url(vapid) as BufferSource })
  const r = await post<{ id: number }>('/api/v1/me/channels', { name, config: { kind: 'webpush', subscription: sub.toJSON() } })
  return r.id
}

export async function unsubscribePush() {
  const sub = await currentSubscription()
  if (sub) await sub.unsubscribe()
}
