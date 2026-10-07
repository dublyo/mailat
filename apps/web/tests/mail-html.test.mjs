import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { JSDOM } from 'jsdom'

// DOMPurify binds to the global window when it is first evaluated.
const require = createRequire(import.meta.url)
globalThis.window = new JSDOM('').window
const out = await build({ entryPoints: [fileURLToPath(new URL('../src/lib/mailHtml.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'cjs', packages: 'external' })
const module = { exports: {} }
new Function('require', 'module', 'exports', out.outputFiles[0].text)(require, module, module.exports)
const { renderMessageDocument, BLOCKED_MESSAGE_CSP, ALLOWED_MESSAGE_CSP } = module.exports

const html = '<p>Hello</p><img src="https://tracker.example/pixel.gif" alt="Logo"><table background="//cdn.example/bg.png"><tr><td style="background:url(https://cdn.example/x.png)">x</td></tr></table><img src="cid:<logo@x>">'

test('blocked mode strips remote src/background, counts them and adds a CSP without https:', () => {
  const { doc, remoteCount } = renderMessageDocument(html, { allowRemote: false })
  assert.equal(remoteCount, 3)
  assert.ok(!/ src="https:\/\/tracker/.test(doc))
  assert.ok(!/ background="\/\/cdn/.test(doc))
  assert.ok(doc.includes('data-mailat-remote-src="https://tracker.example/pixel.gif"'))
  assert.ok(doc.includes('data-mailat-remote-background="//cdn.example/bg.png"'))
  assert.ok(doc.includes(`content="${BLOCKED_MESSAGE_CSP}"`))
  assert.ok(!BLOCKED_MESSAGE_CSP.includes('https:'))
})

test('allowed mode keeps remote images and allows https: in the CSP', () => {
  const { doc, remoteCount } = renderMessageDocument(html, { allowRemote: true })
  assert.equal(remoteCount, 0)
  assert.ok(doc.includes('src="https://tracker.example/pixel.gif"'))
  assert.ok(doc.includes(ALLOWED_MESSAGE_CSP))
  assert.ok(ALLOWED_MESSAGE_CSP.includes('img-src data: blob: https:'))
})

test('cid: references map to the fetched blob URL', () => {
  const { doc } = renderMessageDocument(html, { inlineUrls: { 'logo@x': 'blob:https://app.example/123' } })
  assert.ok(doc.includes('src="blob:https://app.example/123"'))
})

test('scripts and event handlers are removed', () => {
  const { doc } = renderMessageDocument('<p onclick="alert(1)">a</p><script>alert(2)</script><img src="x" onerror="alert(3)"><a href="javascript:alert(4)">b</a>')
  assert.ok(!/<script|onclick|onerror|javascript:/i.test(doc))
})

test('quote mode turns blocked images into [image: alt] text and has no document wrapper', () => {
  const { doc, remoteCount } = renderMessageDocument(html, { mode: 'quote' })
  assert.equal(remoteCount, 3)
  assert.ok(doc.includes('[image: Logo]'))
  assert.ok(!/<img[^>]+https:/.test(doc))
  assert.ok(!doc.includes('<!doctype'))
  assert.ok(doc.includes('cid:'), 'inline references stay for the server to resolve')
  const allowed = renderMessageDocument(html, { mode: 'quote', allowRemote: true })
  assert.ok(allowed.doc.includes('src="https://tracker.example/pixel.gif"'))
  assert.equal(renderMessageDocument('<img src="https://x.example/a.png">', { mode: 'quote' }).doc, '[image]')
})

// Browsers resolve all of these to https://evil.example/p.gif.
const evasions = ['https:\\\\evil.example/p.gif', '\\\\evil.example/p.gif', '/\\evil.example/p.gif', 'ht&#9;tps://evil.example/p.gif']

test('backslash and tab URL tricks are still treated as remote', () => {
  const { isRemoteUrl } = module.exports
  for (const payload of evasions) {
    const html = `<img src="${payload}" alt="t">`
    const view = renderMessageDocument(html)
    assert.equal(view.remoteCount, 1, payload)
    assert.ok(!/<img[^>]* src=/.test(view.doc), `view kept src for ${payload}`)
    const quote = renderMessageDocument(html, { mode: 'quote' })
    assert.equal(quote.doc, '[image: t]', payload)
  }
  for (const local of ['data:image/png;base64,AAAA', 'blob:https://app.example/1', 'x.png', '/a/b.png', '']) assert.equal(isRemoteUrl(local), false, local)
  for (const remote of ['https://x.example/a', '//x.example/a', 'HTTP://x.example', 'https:\\\\x.example', '/\\x.example']) assert.equal(isRemoteUrl(remote), true, remote)
})

test('evasive CSS url() references count toward the reveal banner', () => {
  assert.equal(renderMessageDocument('<div style="background:url(https:\\\\evil.example/x.png)">x</div>').remoteCount, 1)
  assert.equal(renderMessageDocument('<div style="background:url(data:image/png;base64,AAAA)">x</div>').remoteCount, 0)
})

test('signatures are sanitised before compose inserts them', () => {
  const { signatureHtml } = module.exports
  assert.equal(signatureHtml(undefined), '')
  assert.equal(signatureHtml({ signatureHtml: '  ', signatureText: '' }), '')
  const html = signatureHtml({ signatureHtml: '<p onclick="x()">Ibrahim<script>alert(1)</script><img src="https://cdn.test/logo.png"></p><form><input></form>' })
  assert.ok(!/script|onclick|<form|<input/i.test(html), html)
  assert.match(html, /Ibrahim/)
  assert.match(html, /src="https:\/\/cdn\.test\/logo\.png"/)
  assert.equal(signatureHtml({ signatureText: 'Ibrahim\n<CEO> & co' }), '<p>Ibrahim<br>&lt;CEO&gt; &amp; co</p>')
})
