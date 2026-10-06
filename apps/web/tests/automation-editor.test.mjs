import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// Source guard: the editor offers only triggers and steps the executor runs,
// and the API derives the trigger from the trigger step.
const editor = readFileSync(new URL('../src/views/WorkflowEditor.vue', import.meta.url), 'utf8')
const fixtures = JSON.parse(readFileSync(new URL('./fixtures/automation-graphs.json', import.meta.url), 'utf8'))

test('editor offers only supported triggers, actions and conditions', () => {
  for (const unsupported of ['tag.added', 'form.submitted', 'email.opened', 'email.clicked', 'add_tag', 'remove_tag', 'tag_exists', 'contact_added']) {
    assert.ok(!editor.includes(`'${unsupported}'`) && !editor.includes(`"${unsupported}"`), unsupported)
  }
  for (const event of ['contact.subscribed', 'contact.created', 'manual']) {
    assert.match(editor, new RegExp(`<option value="${event.replace('.', '\\.')}">`))
  }
  assert.match(editor, /config: \{ event: 'contact\.subscribed' \}/)
})

test('condition nodes connect only through Yes/No handles', () => {
  assert.match(editor, /v-if="data\.type !== 'condition'"\s+type="source"/)
  assert.match(editor, /v-if="data\.config\?\.mode !== 'filter'" id="no"/)
})

test('shared graph fixtures are well formed', () => {
  assert.ok(fixtures.cases.length >= 30)
  for (const c of fixtures.cases) {
    assert.equal(typeof c.name, 'string')
    assert.ok(Array.isArray(c.workflow.nodes) && Array.isArray(c.workflow.edges), c.name)
    for (const pair of [...(c.structure || []), ...(c.publish || [])]) {
      assert.ok(Array.isArray(pair) && pair.length === 2, c.name)
    }
  }
  assert.ok(fixtures.cases.some(c => c.publish?.length === 0))
})
