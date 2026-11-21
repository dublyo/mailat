// Login PIN helpers: the 4-digit second step after the password, and the
// "remember this device" tokens that skip it for 30 days.

export const PIN_LENGTH = 4

/**
 * Mirrors the server rule: exactly 4 digits, not all the same (1111) and not
 * a run up or down (1234, 4321). Returns an error message, or '' when valid.
 */
export function pinProblem(pin: string): string {
  if (!/^[0-9]{4}$/.test(pin)) return 'Enter exactly 4 digits.'
  const steps = [1, 2, 3].map(i => pin.charCodeAt(i) - pin.charCodeAt(i - 1))
  if (steps.every(d => d === 0)) return 'Avoid the same digit four times.'
  if (steps.every(d => d === 1) || steps.every(d => d === -1)) return 'Avoid runs like 1234 or 4321.'
  return ''
}

/** Keeps digits only, at most `max` of them (for pasted text). */
export function pinDigits(text: string, max = PIN_LENGTH): string[] {
  return (text.match(/[0-9]/g) || []).slice(0, max)
}

const DEVICE_KEY = 'mailat.trustedDevices'

type Storage = Pick<globalThis.Storage, 'getItem' | 'setItem'>

function browserStorage(): Storage | null {
  try {
    return typeof localStorage === 'undefined' ? null : localStorage
  } catch {
    return null
  }
}

type DeviceMap = Record<string, { token: string; expiresAt?: string }>

function readDevices(storage: Storage | null): DeviceMap {
  try {
    const parsed = JSON.parse(storage?.getItem(DEVICE_KEY) || '{}')
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {}
  } catch {
    return {}
  }
}

function writeDevices(storage: Storage | null, devices: DeviceMap) {
  try {
    storage?.setItem(DEVICE_KEY, JSON.stringify(devices))
  } catch {
    // Private mode or blocked storage: the PIN is simply asked next time.
  }
}

const key = (email: string) => email.trim().toLowerCase()

/** The remembered-device token for this login email, if any and unexpired. */
export function deviceTokenFor(email: string, storage: Storage | null = browserStorage(), now = Date.now()): string | undefined {
  const entry = readDevices(storage)[key(email)]
  if (!entry || typeof entry.token !== 'string') return undefined
  if (entry.expiresAt && Date.parse(entry.expiresAt) <= now) return undefined
  return entry.token
}

/** Stores the token returned by a PIN step that asked to remember the device. */
export function rememberDevice(email: string, token: string, expiresAt?: string, storage: Storage | null = browserStorage(), now = Date.now()) {
  const devices = readDevices(storage)
  for (const [k, v] of Object.entries(devices)) {
    if (v?.expiresAt && Date.parse(v.expiresAt) <= now) delete devices[k]
  }
  devices[key(email)] = { token, expiresAt }
  writeDevices(storage, devices)
}

/** Drops this browser's token for the email (the server forgot it). */
export function forgetDevice(email: string, storage: Storage | null = browserStorage()) {
  const devices = readDevices(storage)
  if (!(key(email) in devices)) return
  delete devices[key(email)]
  writeDevices(storage, devices)
}

/** User-facing text for a failed PIN request. */
export function pinErrorMessage(err: unknown): string {
  const e = err as { status?: number; message?: string; data?: { attemptsLeft?: number } }
  if (e?.status === 429) return e.message || 'Too many wrong PIN attempts. Try again later.'
  if (e?.status === 401 && typeof e.data?.attemptsLeft === 'number') {
    return e.data.attemptsLeft > 0 ? e.message || 'Wrong PIN.' : 'Wrong PIN. Sign in again to try once more.'
  }
  return e?.message || 'Could not check the PIN. Try again.'
}
