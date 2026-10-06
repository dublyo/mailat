// Binds an OAuth sign-in to the tab that started it. The API echoes the nonce
// in the /login#session= fragment; a fragment without the matching nonce (for
// example a link carrying someone else's session) is never adopted.

const KEY = 'mailat_oauth_nonce'

type NonceStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

function defaultStorage(): NonceStorage | null {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

export function createOAuthNonce(storage: NonceStorage | null = defaultStorage()): string {
  const bytes = crypto.getRandomValues(new Uint8Array(24))
  const nonce = Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')
  try {
    storage?.setItem(KEY, nonce)
  } catch {
    // Without storage the fragment cannot be verified and will be refused.
  }
  return nonce
}

/** Returns true only when received matches the stored nonce; the nonce is single use. */
export function consumeOAuthNonce(received: string | null, storage: NonceStorage | null = defaultStorage()): boolean {
  let expected: string | null = null
  try {
    expected = storage?.getItem(KEY) ?? null
    storage?.removeItem(KEY)
  } catch {
    return false
  }
  return !!expected && !!received && expected === received
}
