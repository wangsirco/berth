// Adapted from github.com/wangsirco/wharf (web/src/lib/passkey.ts), Copyright (c) Wharf authors, MIT License.
// WebAuthn in the browser: the server sends options with base64url fields; the browser API
// wants ArrayBuffers. Recent browsers convert themselves; the fallback does it by hand.
import { post } from './api'
import type { User } from './types'

const b64u = {
  dec: (s: string) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(s.length / 4) * 4, '=')), (c) => c.charCodeAt(0)).buffer,
  enc: (b: ArrayBuffer) => btoa(String.fromCharCode(...new Uint8Array(b))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''),
}

type Json = Record<string, unknown>
const PKC = () => window.PublicKeyCredential as unknown as {
  parseCreationOptionsFromJSON?: (o: Json) => PublicKeyCredentialCreationOptions
  parseRequestOptionsFromJSON?: (o: Json) => PublicKeyCredentialRequestOptions
} | undefined

export const passkeySupported = () => typeof window !== 'undefined' && !!window.PublicKeyCredential && window.isSecureContext
// Browsers refuse passkeys on IP addresses: the panel must be opened by name.
export const onIPAddress = () => /^(\d+\.){3}\d+$|^\[?[0-9a-f:]+\]?$/i.test(location.hostname)

function creationOptions(o: Json): PublicKeyCredentialCreationOptions {
  const f = PKC()?.parseCreationOptionsFromJSON
  if (f) return f(o)
  const user = o.user as Json
  return {
    ...(o as unknown as PublicKeyCredentialCreationOptions),
    challenge: b64u.dec(o.challenge as string),
    user: { ...(user as unknown as PublicKeyCredentialUserEntity), id: b64u.dec(user.id as string) },
    excludeCredentials: ((o.excludeCredentials as Json[] | undefined) ?? []).map((c) => ({ ...(c as unknown as PublicKeyCredentialDescriptor), id: b64u.dec(c.id as string) })),
  }
}

function requestOptions(o: Json): PublicKeyCredentialRequestOptions {
  const f = PKC()?.parseRequestOptionsFromJSON
  if (f) return f(o)
  return {
    ...(o as unknown as PublicKeyCredentialRequestOptions),
    challenge: b64u.dec(o.challenge as string),
    allowCredentials: ((o.allowCredentials as Json[] | undefined) ?? []).map((c) => ({ ...(c as unknown as PublicKeyCredentialDescriptor), id: b64u.dec(c.id as string) })),
  }
}

function toJSON(c: PublicKeyCredential): Json {
  const j = (c as unknown as { toJSON?: () => Json }).toJSON
  if (j) return j.call(c)
  const r = c.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse
  const response: Json = { clientDataJSON: b64u.enc(r.clientDataJSON) }
  if (r.attestationObject) {
    response.attestationObject = b64u.enc(r.attestationObject)
    response.transports = r.getTransports?.() ?? []
  }
  if (r.authenticatorData) {
    response.authenticatorData = b64u.enc(r.authenticatorData)
    response.signature = b64u.enc(r.signature)
    if (r.userHandle) response.userHandle = b64u.enc(r.userHandle)
  }
  return { id: c.id, rawId: b64u.enc(c.rawId), type: c.type, response, clientExtensionResults: c.getClientExtensionResults(), authenticatorAttachment: c.authenticatorAttachment }
}

export class PasskeyCancelled extends Error {}
const cancelled = (e: unknown) => e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError')

export async function addPasskey(name: string) {
  const b = await post<{ flow: string; options: Json }>('/api/v1/me/passkeys/begin', { name })
  let cred: PublicKeyCredential
  try {
    cred = (await navigator.credentials.create({ publicKey: creationOptions(b.options) })) as PublicKeyCredential
  } catch (e) {
    if (cancelled(e)) throw new PasskeyCancelled()
    throw e
  }
  return post(`/api/v1/me/passkeys/finish?flow=${b.flow}`, toJSON(cred))
}

export function defaultPasskeyName() {
  const ua = navigator.userAgent
  if (/iPhone/.test(ua)) return 'iPhone'
  if (/iPad/.test(ua)) return 'iPad'
  if (/Android/.test(ua)) return 'Android'
  if (/Macintosh/.test(ua)) return 'Mac'
  if (/Windows/.test(ua)) return 'Windows'
  return 'Passkey'
}

// passkeyLogin: a discoverable sign-in, or (with a half-signed-in session) the second step.
export async function passkeyLogin(remember = false) {
  const b = await post<{ flow: string; options: Json }>('/api/v1/auth/passkey/begin', {})
  let cred: PublicKeyCredential
  try {
    cred = (await navigator.credentials.get({ publicKey: requestOptions(b.options) })) as PublicKeyCredential
  } catch (e) {
    if (cancelled(e)) throw new PasskeyCancelled()
    throw e
  }
  return post<{ user: User; csrf: string }>(`/api/v1/auth/passkey/finish?flow=${b.flow}${remember ? '&remember=1' : ''}`, toJSON(cred))
}
