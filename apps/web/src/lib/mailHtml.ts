import DOMPurify from 'dompurify'

// Renders untrusted message HTML for the sandboxed srcdoc iframe (view) or for
// quoting into the composer (quote). Remote content stays blocked unless the
// caller allows it: a sanitize hook strips remote URLs, and in view mode a meta
// CSP enforces the same decision even if sanitizing misses something.

export interface RenderMessageOptions {
  /** Content-ID (without angle brackets) to an already-fetched blob: URL. */
  inlineUrls?: Record<string, string>
  allowRemote?: boolean
  mode?: 'view' | 'quote'
}

export interface RenderedMessage {
  doc: string
  remoteCount: number
}

export const BLOCKED_MESSAGE_CSP = "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:"
export const ALLOWED_MESSAGE_CSP = "default-src 'none'; img-src data: blob: https:; style-src 'unsafe-inline'; font-src data: https:"

const PURIFY_CONFIG = {
  FORBID_TAGS: ['style', 'form', 'input', 'button'],
  FORBID_ATTR: ['srcset'],
  ALLOWED_URI_REGEXP: /^(?:(?:https?|mailto|cid|blob):|[^a-z]|[a-z+.-]+(?:[^a-z+.-:]|$))/i,
}
const URL_ATTRIBUTES = new Set(['src', 'background', 'poster'])
const REMOTE_URL = /^(?:https?:)?\/\//i
const REMOTE_CSS_URL = /url\(\s*['"]?\s*(?:https?:)?\/\//i
const BASE_STYLE = 'body{font:14px/1.6 system-ui;color:#1f2937;margin:12px;overflow-wrap:anywhere}img,table{max-width:100%}img{height:auto}pre{white-space:pre-wrap}a{color:#2563eb}'

function escapeAttribute(value: string) {
  return value.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

export function renderMessageDocument(html: string, options: RenderMessageOptions = {}): RenderedMessage {
  const { inlineUrls = {}, allowRemote = false, mode = 'view' } = options
  let remoteCount = 0
  const hidden: Array<[Element, string, string]> = []

  const hook = (node: Element, data: { attrName: string; attrValue: string; keepAttr: boolean }) => {
    if (URL_ATTRIBUTES.has(data.attrName)) {
      const value = data.attrValue.trim()
      if (/^cid:/i.test(value)) {
        const url = inlineUrls[value.slice(4).replace(/[<>]/g, '')]
        if (url && mode === 'view') data.attrValue = url
        return
      }
      if (allowRemote || !REMOTE_URL.test(value)) return
      remoteCount++
      data.keepAttr = false
      hidden.push([node, data.attrName, value])
    } else if (data.attrName === 'style' && !allowRemote && REMOTE_CSS_URL.test(data.attrValue)) {
      // The CSP blocks CSS url() loads; count them so the reveal banner appears.
      remoteCount++
    }
  }
  DOMPurify.addHook('uponSanitizeAttribute', hook)
  let fragment: DocumentFragment
  try {
    fragment = DOMPurify.sanitize(html || '', { ...PURIFY_CONFIG, RETURN_DOM_FRAGMENT: true })
  } finally {
    DOMPurify.removeHook('uponSanitizeAttribute', hook)
  }
  for (const [node, name, value] of hidden) node.setAttribute(`data-mailat-remote-${name}`, value)

  const container = fragment.ownerDocument.createElement('div')
  container.appendChild(fragment)
  if (mode === 'quote') {
    // Never load trackers in the app origin: blocked images become text.
    for (const image of Array.from(container.querySelectorAll('img[data-mailat-remote-src]'))) {
      const alt = (image.getAttribute('alt') || '').trim()
      image.replaceWith(container.ownerDocument.createTextNode(alt ? `[image: ${alt}]` : '[image]'))
    }
    return { doc: container.innerHTML, remoteCount }
  }
  const csp = allowRemote ? ALLOWED_MESSAGE_CSP : BLOCKED_MESSAGE_CSP
  const doc = `<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="${escapeAttribute(csp)}"><meta name="viewport" content="width=device-width,initial-scale=1"><base target="_blank"><style>${BASE_STYLE}</style></head><body>${container.innerHTML}</body></html>`
  return { doc, remoteCount }
}
