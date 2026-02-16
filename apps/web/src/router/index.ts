import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
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
      meta: { requiresAuth: true }
    },
    {
      path: '/inbox/:folder',
      name: 'inbox-folder',
      component: () => import('@/views/ReceivedInbox.vue'),
      meta: { requiresAuth: true }
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
      meta: { requiresAuth: true }
    },
    {
      path: '/received/:folder',
      name: 'received-inbox-folder',
      component: () => import('@/views/ReceivedInbox.vue'),
      meta: { requiresAuth: true }
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
      path: '/health',
      name: 'health',
      component: () => import('@/views/Health.vue'),
      meta: { requiresAuth: true }
    },
    {
      path: '/settings',
      name: 'settings',
      component: () => import('@/views/Settings.vue'),
      meta: { requiresAuth: true }
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
  const authStore = useAuthStore()

  // Check if we need to restore auth state
  if (!authStore.isInitialized) {
    await authStore.checkAuth()
  }

  const requiresAuth = to.matched.some(record => record.meta.requiresAuth)
  const isGuestRoute = to.matched.some(record => record.meta.guest)

  if (requiresAuth && !authStore.isAuthenticated) {
    next({ name: 'login', query: { redirect: to.fullPath } })
  } else if (isGuestRoute && authStore.isAuthenticated) {
    next({ name: 'inbox' })
  } else {
    next()
  }
})

export default router
