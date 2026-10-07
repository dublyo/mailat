import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { fileURLToPath } from 'node:url'

const webRoot = fileURLToPath(new URL('..', import.meta.url))
function evaluate(code) { const module = { exports: {} }; new Function('module', 'exports', code)(module, module.exports); return module.exports }

const roles = evaluate((await build({ entryPoints: [`${webRoot}/src/lib/roles.ts`], bundle: true, write: false, platform: 'node', format: 'cjs' })).outputFiles[0].text)

// The real route table and guard, with vue-router, views and the auth store stubbed.
const routerModule = evaluate((await build({
  entryPoints: [`${webRoot}/src/router/index.ts`], bundle: true, write: false, platform: 'node', format: 'cjs',
  define: { 'import.meta.env.BASE_URL': '"/"' },
  plugins: [{ name: 'router-fixture', setup(builder) {
    builder.onResolve({ filter: /^vue-router$/ }, () => ({ path: 'vue-router', namespace: 'fixture' }))
    builder.onResolve({ filter: /^@\/stores\/auth$/ }, () => ({ path: 'auth', namespace: 'fixture' }))
    builder.onResolve({ filter: /^@\/views\// }, () => ({ path: 'view', namespace: 'fixture' }))
    builder.onResolve({ filter: /^@\/lib\/roles$/ }, () => ({ path: `${webRoot}/src/lib/roles.ts` }))
    builder.onLoad({ filter: /.*/, namespace: 'fixture' }, args => ({ loader: 'js', contents: {
      'vue-router': 'export const createWebHistory = () => null; export const createRouter = options => ({ options, beforeEach(fn) { this.guard = fn } })',
      auth: 'export const useAuthStore = () => globalThis.__authFixture',
      view: 'export default {}',
    }[args.path] }))
  } }],
})).outputFiles[0].text)
const router = routerModule.default

function resolve(fullPath) {
  const path = fullPath.split('?')[0]
  const record = router.options.routes.find(r => {
    const a = r.path.split('/'), b = path.split('/')
    return a.length === b.length && a.every((part, i) => part.startsWith(':') || part === b[i])
  })
  assert.ok(record, `no route for ${path}`)
  return { path, fullPath, name: record.name, meta: record.meta ?? {}, matched: [record] }
}

async function navigate(fullPath, user) {
  globalThis.window = { top: 1, self: 1 }
  globalThis.__authFixture = { isInitialized: true, isAuthenticated: !!user, user, checkAuth: async () => {} }
  let result = 'unset'
  await router.guard(resolve(fullPath), null, to => { result = to === undefined ? fullPath : to })
  return typeof result === 'object' ? (result.name === 'login' ? '/login' : result.name) : result
}

const mailbox = { role: 'mailbox' }, member = { role: 'member' }, admin = { role: 'admin' }, owner = { role: 'owner' }

test('mailbox users are sent to /received from every page not marked for them', async () => {
  for (const path of ['/campaigns', '/campaigns/x', '/automations', '/contacts', '/forms', '/domains', '/health', '/jmap-inbox', '/api-docs']) {
    assert.equal(await navigate(path, mailbox), '/received', path)
  }
  // After sign-in, Login pushes ?redirect=/contacts; the guard still applies.
  assert.equal(await navigate('/contacts', mailbox), '/received')
  for (const path of ['/inbox', '/inbox/sent', '/received', '/received/inbox', '/settings']) {
    assert.equal(await navigate(path, mailbox), path, path)
  }
})

test('admin-only pages are blocked for members, staff pages are not', async () => {
  assert.equal(await navigate('/health', member), '/received')
  assert.equal(await navigate('/health', admin), '/health')
  assert.equal(await navigate('/health', owner), '/health')
  for (const path of ['/campaigns', '/domains', '/api-docs', '/jmap-inbox', '/settings']) {
    assert.equal(await navigate(path, member), path, path)
  }
})

test('mailbox admin pages are for owners and admins only', async () => {
  const uuid = '0d6c1c1e-8b7a-4b8e-9e2f-6a1f2b3c4d5e'
  for (const path of [`/domains/${uuid}/mailboxes`, `/domains/${uuid}/mailboxes/${uuid}`]) {
    assert.equal(await navigate(path, mailbox), '/received', path)
    assert.equal(await navigate(path, member), '/received', path)
    assert.equal(await navigate(path, admin), path, path)
    assert.equal(await navigate(path, owner), path, path)
  }
})

test('public and guest routes keep their behaviour', async () => {
  assert.equal(await navigate('/invite', mailbox), '/invite')
  assert.equal(await navigate('/login', null), '/login')
  assert.equal(await navigate('/campaigns', null), '/login')
  assert.equal(await navigate('/login', mailbox), 'inbox')
})

test('role helpers fail closed', () => {
  const { isOrgAdmin, isMailboxUser, isStaff, canSee, deniedRedirect } = roles
  assert.equal(isOrgAdmin(mailbox), false)
  assert.equal(isMailboxUser(mailbox), true)
  assert.equal(isStaff(mailbox), false)
  assert.equal(isStaff(member), true)
  assert.equal(isStaff(null), false)
  assert.equal(canSee(mailbox), false, 'the default audience is staff')
  assert.equal(canSee(mailbox, 'all'), true)
  assert.equal(canSee(member, 'admin'), false)
  assert.equal(canSee(null, 'all'), false)
  assert.equal(deniedRedirect({}, mailbox), '/received')
  assert.equal(deniedRedirect({ mailbox: true, admin: true }, mailbox), '/received')
  assert.equal(deniedRedirect({}, member), null)
})

test('settings tabs per role', () => {
  const tabs = ['general', 'signature', 'security', 'notifications', 'appearance', 'filters', 'vacation', 'shared', 'team', 'integrations', 'future-tab']
  const visible = (user, shared) => tabs.filter(id => roles.settingsTabVisible(id, user, shared))
  const mail = ['general', 'signature', 'security', 'notifications', 'appearance', 'filters', 'vacation']
  assert.deepEqual(visible(mailbox, false), mail)
  assert.deepEqual(visible(mailbox, true), [...mail, 'shared'])
  assert.deepEqual(visible(member, false), [...mail, 'shared', 'integrations', 'future-tab'])
  assert.deepEqual(visible(admin, false), tabs)
})

test('Settings shows the Signature tab and hides sign-in providers and staff notifications from mailbox users', async () => {
  const { readFile } = await import('node:fs/promises')
  const source = await readFile(`${webRoot}/src/views/Settings.vue`, 'utf8')
  assert.match(source, /\{ id: 'signature', label: 'Signature'/)
  assert.match(source, /activeTab === 'signature'[\s\S]{0,80}<SignatureSettings \/>/)
  // The OAuth block is gated on staff, and providers are never fetched for mailbox users.
  assert.match(source, /<section v-if="isStaff\(authStore\.user\) && \(oauthProviders\.length/)
  assert.match(source, /if \(!isStaff\(authStore\.user\)\) return\s+fetchWebhooks\(\)[\s\S]*fetchSignInProviders\(\)/)
  // Campaign reports and org health alerts are staff-only notification choices.
  assert.match(source, /<label v-if="isStaff\(authStore\.user\)"[^>]*>\s*<input\s+type="checkbox"\s+v-model="settingsStore\.settings\.campaignReports"/)
  assert.match(source, /<section v-if="isStaff\(authStore\.user\)"[^>]*>\s*<h3[^>]*>Alert Notifications<\/h3>/)
})

test('inbox domain filter: mailbox users use identity domains, never the domain list', async () => {
  const domains = [{ id: 1, name: 'vayb.dev' }, { id: 2, name: 'other.test' }]
  const identities = [{ domainId: 1, email: 'ibrahim@vayb.dev' }, { domainId: 1, email: 'sales@vayb.dev' }, { domainId: 3, email: 'team@shared.test' }]
  assert.deepEqual(roles.inboxDomainOptions(mailbox, domains, identities), [{ id: '1', name: 'vayb.dev' }, { id: '3', name: 'shared.test' }])
  assert.deepEqual(roles.inboxDomainOptions(member, domains, identities), [{ id: '1', name: 'vayb.dev' }, { id: '2', name: 'other.test' }])
  const { readFile } = await import('node:fs/promises')
  const source = await readFile(`${webRoot}/src/views/ReceivedInbox.vue`, 'utf8')
  assert.match(source, /isMailboxUser\(authStore\.user\) \? null : domains\.fetchDomains\(\)/)
  assert.ok(!/v-for="domain in domains\.domains"/.test(source), 'the filter reads domainOptions')
})

test('sidebar items per role', async () => {
  const { readFile } = await import('node:fs/promises')
  const source = await readFile(`${webRoot}/src/components/layout/Sidebar.vue`, 'utf8')
  const items = [...source.matchAll(/\{ id: '([\w-]+)'[^}]*?audience: '(\w+)' \}/g)].map(m => ({ id: m[1], audience: m[2] }))
  assert.ok(items.length === 14, 'every sidebar item declares an audience')
  const ids = user => roles.visibleFor(items, user).map(i => i.id)
  assert.deepEqual(ids(mailbox), ['inbox', 'all', 'starred', 'sent', 'drafts', 'outbox'])
  assert.ok(ids(member).includes('dmarc-reports') && ids(member).includes('campaigns') && !ids(member).includes('health'))
  assert.ok(ids(admin).includes('health'))
})
