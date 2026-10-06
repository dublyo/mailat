import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// Source guard: campaign progress is polled with the bearer header, never streamed
// with a JWT in the URL (URLs land in proxy logs and browser history).
const source = readFileSync(new URL('../src/views/CampaignDetail.vue', import.meta.url), 'utf8')

test('campaign detail polls instead of opening an EventSource', () => {
  assert.doesNotMatch(source, /EventSource/)
  assert.doesNotMatch(source, /localStorage/)
  assert.doesNotMatch(source, /[?&]token=/)
  assert.match(source, /POLL_INTERVAL_MS = 5000/)
  assert.match(source, /setInterval\(pollCampaign, POLL_INTERVAL_MS\)/)
  assert.match(source, /onUnmounted\(\(\) => \{\s*stopPolling\(\)/)
})
