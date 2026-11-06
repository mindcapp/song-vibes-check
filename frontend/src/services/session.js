// One anonymous listening session per browser tab. The id lives in
// sessionStorage, so it survives reloads of the tab but not a new tab. The API
// accepts 8-64 characters of letters, digits, '-' and '_'; a UUID fits.

const SESSION_KEY = 'vibeSessionId'

let memoryId = null // used when sessionStorage is unavailable (e.g. blocked)

function newId() {
  if (globalThis.crypto?.randomUUID) {
    return globalThis.crypto.randomUUID()
  }
  // randomUUID needs a secure context; plain http on a LAN address lacks one.
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
}

export function getSessionId(storage = globalThis.sessionStorage) {
  try {
    let id = storage.getItem(SESSION_KEY)
    if (!id) {
      id = newId()
      storage.setItem(SESSION_KEY, id)
    }
    return id
  } catch {
    memoryId ??= newId()
    return memoryId
  }
}
