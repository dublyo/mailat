// Web push on this device: the service worker subscription and its state
// against the server's VAPID key and active subscriptions.

export type PushState =
  | 'unsupported' // no service worker or Push API in this browser
  | 'disabled' // the server has no VAPID keys
  | 'denied' // the user blocked notifications for this site
  | 'off' // not subscribed on this device
  | 'stale' // subscribed with an old key, or the server dropped it: re-enable
  | 'on'

export function base64UrlToBytes(value: string): Uint8Array {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(value.length / 4) * 4, '=')
  const binary = atob(base64)
  return Uint8Array.from(binary, char => char.charCodeAt(0))
}

export function bytesToBase64Url(value: ArrayBuffer | Uint8Array | null | undefined): string {
  if (!value) return ''
  const bytes = value instanceof Uint8Array ? value : new Uint8Array(value)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

type BrowserSubscription = Pick<PushSubscription, 'endpoint' | 'getKey'> & { options?: { applicationServerKey?: ArrayBuffer | null } }

/** The body for POST /push/subscribe. */
export function subscriptionBody(subscription: BrowserSubscription, deviceName: string) {
  return {
    endpoint: subscription.endpoint,
    p256dhKey: bytesToBase64Url(subscription.getKey('p256dh')),
    authKey: bytesToBase64Url(subscription.getKey('auth')),
    deviceName: deviceName.slice(0, 100),
  }
}

export function pushState(input: {
  supported: boolean
  enabled: boolean
  publicKey: string
  permission: NotificationPermission
  subscription: BrowserSubscription | null
  serverEndpoints: string[]
}): PushState {
  if (!input.supported) return 'unsupported'
  if (!input.enabled || !input.publicKey) return 'disabled'
  if (input.permission === 'denied') return 'denied'
  if (!input.subscription) return 'off'
  const key = bytesToBase64Url(input.subscription.options?.applicationServerKey)
  if ((key && key !== input.publicKey.replace(/=+$/, '')) || !input.serverEndpoints.includes(input.subscription.endpoint)) return 'stale'
  return 'on'
}

export function pushSupported() {
  return typeof window !== 'undefined' && 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

/** A short label for this browser, shown in the device list. */
export function deviceLabel(userAgent = typeof navigator === 'undefined' ? '' : navigator.userAgent) {
  const browser = /Edg\//.test(userAgent) ? 'Edge' : /Firefox\//.test(userAgent) ? 'Firefox' : /Chrome\//.test(userAgent) ? 'Chrome' : /Safari\//.test(userAgent) ? 'Safari' : 'Browser'
  const os = /Android/.test(userAgent) ? 'Android' : /iPhone|iPad/.test(userAgent) ? 'iOS' : /Mac OS X/.test(userAgent) ? 'macOS' : /Windows/.test(userAgent) ? 'Windows' : /Linux/.test(userAgent) ? 'Linux' : ''
  return os ? `${browser} on ${os}` : browser
}
