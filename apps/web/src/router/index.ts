import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { deniedRedirect } from '@/lib/roles'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    { path: '/unsubscribe', name: 'public-unsubscribe', component: () => import('@/views/PublicUnsubscribe.vue'), meta: { publicSignup: true } },
    { path: '/forms', name: 'forms', component: () => import('@/views/SignupForms.vue'), meta: { requiresAuth: true } },
    { path: '/subscribe/confirm', name: 'signup-confirm', component: () => import('@/views/PublicSignup.vue'), meta: { publicSignup: true } },
    { path: '/subscribe/:uuid', name: 'public-signup', component: () => import('@/views/PublicSignup.vue'), meta: { publicSignup: true } },
    // Public pages open from email links. They work signed in or out, and are
    // never redirected; the session is still restored so the page knows who is here.
    { path: '/invite', name: 'accept-invite', component: () => import('@/views/AcceptInvite.vue'), meta: { public: true } },
    { path: '/forwards/verify', name: 'verify-forward', component: () => import('@/views/VerifyForward.vue'), meta: { public: true } },
    {
      path: '/',
      redirect: '/inbox'
    },
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/Login.vue'),
      meta: { guest: true }
    },
    {
      path: '/register',
      name: 'register',
      component: () => import('@/views/Register.vue'),
      meta: { guest: true }
    },
    {
      path: '/inbox',
      name: 'inbox',
      component: () => import('@/views/ReceivedInbox.vue'),
      meta: { requiresAuth: true, mailbox: true }
    },
    {
      path: '/inbox/:folder',
      name: 'inbox-folder',
      component: () => import('@/views/ReceivedInbox.vue'),
      meta: { requiresAuth: true, mailbox: true }
    },
    {
      path: '/jmap-inbox',
      name: 'jmap-inbox',
      component: () => import('@/views/Inbox.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/received',
      name: 'received-inbox',
      component: () => import('@/views/ReceivedInbox.vue'),
      meta: { requiresAuth: true, mailbox: true }
    },
    {
      path: '/received/:folder',
      name: 'received-inbox-folder',
      component: () => import('@/views/ReceivedInbox.vue'),
      meta: { requiresAuth: true, mailbox: true }
    },
    {
      path: '/campaigns',
      name: 'campaigns',
      component: () => import('@/views/Campaigns.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/campaigns/:uuid',
      name: 'campaign-detail',
      component: () => import('@/views/CampaignDetail.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/automations',
      name: 'automations',
      component: () => import('@/views/Automations.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/automations/new',
      name: 'automation-new',
      component: () => import('@/views/WorkflowEditor.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/automations/:uuid',
      name: 'automation-edit',
      component: () => import('@/views/WorkflowEditor.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/contacts',
      name: 'contacts',
      component: () => import('@/views/Contacts.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/domains',
      name: 'domains',
      component: () => import('@/views/Domains.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/domains/:uuid/mailboxes',
      name: 'domain-mailboxes',
      component: () => import('@/views/DomainMailboxes.vue'),
      meta: { requiresAuth: true, admin: true }
    },
    {
      path: '/domains/:uuid/mailboxes/:userUuid',
      name: 'mailbox-detail',
      component: () => import('@/views/MailboxDetail.vue'),
      meta: { requiresAuth: true, admin: true }
    },
    {
      path: '/health',
      name: 'health',
      component: () => import('@/views/Health.vue'),
      meta: { requiresAuth: true, admin: true }
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('@/views/Settings.vue'),
      meta: { requiresAuth: true, mailbox: true }
    },
    {
      path: '/api-docs',
      name: 'api-docs',
      component: () => import('@/views/API.vue'),
      meta: { requiresAuth: true }
    }
  ]
})

router.beforeEach(async (to, _from, next) => {
  // An embedded public document must never navigate into authenticated SPA views.
  if (window.top !== window.self && to.name !== 'public-signup') { next(false); return }
  // Embedded signup pages work without cookies, localStorage or a login session.
  if (to.meta.publicSignup) { next(); return }
  const authStore = useAuthStore()

  // Check if we need to restore auth state
  if (!authStore.isInitialized) {
    await authStore.checkAuth()
  }

  if (to.meta.public) { next(); return }

  const requiresAuth = to.matched.some(record => record.meta.requiresAuth)
  const isGuestRoute = to.matched.some(record => record.meta.guest)

  if (requiresAuth && !authStore.isAuthenticated) {
    next({ name: 'login', query: { redirect: to.fullPath } })
  } else if (isGuestRoute && authStore.isAuthenticated) {
    next({ name: 'inbox' })
  } else {
    // Fail-closed: mailbox users open only pages marked `mailbox`; `admin`
    // pages need an owner or admin. This also covers a login ?redirect=.
    const denied = requiresAuth ? deniedRedirect(to.meta, authStore.user) : null
    if (denied && denied !== to.path) next(denied)
    else next()
  }
})

export default router
