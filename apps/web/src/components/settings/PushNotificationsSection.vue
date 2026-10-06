<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { Trash2 } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import { pushApi, type PushSubscriptionInfo } from '@/lib/api'
import { base64UrlToBytes, deviceLabel, pushState, pushSupported, subscriptionBody, type PushState } from '@/lib/push'

const loading = ref(true)
const busy = ref(false)
const error = ref('')
const enabled = ref(false)
const publicKey = ref('')
const devices = ref<PushSubscriptionInfo[]>([])
const local = ref<PushSubscription | null>(null)
const permission = ref<NotificationPermission>(typeof Notification === 'undefined' ? 'default' : Notification.permission)
const supported = pushSupported()

const state = computed<PushState>(() => pushState({
  supported, enabled: enabled.value, publicKey: publicKey.value, permission: permission.value,
  subscription: local.value, serverEndpoints: devices.value.map(d => d.endpoint),
}))
const thisDevice = computed(() => local.value?.endpoint)

async function registration() {
  return navigator.serviceWorker.register('/sw.js', { scope: '/' })
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [key, list] = await Promise.all([pushApi.vapidKey(), pushApi.list()])
    enabled.value = key.enabled
    publicKey.value = key.publicKey
    devices.value = list ?? []
    if (supported && key.enabled) {
      const reg = await navigator.serviceWorker.getRegistration('/')
      local.value = (await reg?.pushManager.getSubscription()) ?? null
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not load notification settings.'
  } finally {
    loading.value = false
  }
}

async function enable() {
  busy.value = true
  error.value = ''
  try {
    permission.value = await Notification.requestPermission()
    if (permission.value !== 'granted') {
      error.value = 'Notifications are blocked for this site. Allow them in your browser settings, then try again.'
      return
    }
    const reg = await registration()
    await navigator.serviceWorker.ready
    // A subscription made with a previous server key can never be delivered.
    const existing = await reg.pushManager.getSubscription()
    if (existing) {
      await pushApi.unsubscribe(existing.endpoint).catch(() => {})
      await existing.unsubscribe()
    }
    const subscription = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: base64UrlToBytes(publicKey.value) as BufferSource })
    await pushApi.subscribe(subscriptionBody(subscription, deviceLabel()))
    local.value = subscription
    devices.value = (await pushApi.list()) ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not turn on notifications for this device.'
  } finally {
    busy.value = false
  }
}

async function disable() {
  busy.value = true
  error.value = ''
  try {
    const subscription = local.value
    if (subscription) {
      await pushApi.unsubscribe(subscription.endpoint).catch(() => {})
      await subscription.unsubscribe()
    }
    local.value = null
    devices.value = (await pushApi.list()) ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not turn off notifications for this device.'
  } finally {
    busy.value = false
  }
}

async function removeDevice(device: PushSubscriptionInfo) {
  if (device.endpoint === thisDevice.value) return disable()
  busy.value = true
  try {
    await pushApi.unsubscribe(device.endpoint)
    devices.value = devices.value.filter(d => d.uuid !== device.uuid)
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not remove this device.'
  } finally {
    busy.value = false
  }
}

async function toggleNewMail(device: PushSubscriptionInfo) {
  try {
    await pushApi.setNewEmail(device.uuid, !device.notifyNewEmail)
    device.notifyNewEmail = !device.notifyNewEmail
  } catch (e) {
    error.value = e instanceof Error ? e.message : 'Could not update this device.'
  }
}

onMounted(load)
</script>

<template>
  <section class="pt-6 border-t border-gmail-border" aria-labelledby="push-title" :aria-busy="loading || busy">
    <h3 id="push-title" class="text-sm font-medium text-gmail-gray mb-4">Desktop notifications on this device</h3>
    <div class="flex items-center justify-between gap-4 p-4 bg-gmail-lightGray rounded-lg">
      <div class="text-sm">
        <p v-if="loading" role="status" class="text-gmail-gray">Checking this device…</p>
        <template v-else>
          <p class="font-medium">New mail in your Inbox</p>
          <p v-if="state === 'unsupported'" class="text-gmail-gray">This browser does not support push notifications. On iPhone and iPad, add Mailat to the Home Screen first.</p>
          <p v-else-if="state === 'disabled'" class="text-gmail-gray">Push is not configured on this server (VAPID keys).</p>
          <p v-else-if="state === 'denied'" class="text-gmail-gray">Notifications are blocked for this site in your browser settings.</p>
          <p v-else-if="state === 'stale'" class="text-amber-800">The server's notification key changed. Re-enable on this device.</p>
          <p v-else-if="state === 'on'" class="text-gmail-gray">Shows the sender and subject, even when Mailat is closed.</p>
          <p v-else class="text-gmail-gray">Get a notification with the sender and subject when mail arrives.</p>
        </template>
      </div>
      <template v-if="!loading">
        <Button v-if="state === 'off' || state === 'stale'" variant="secondary" :disabled="busy" @click="enable">{{ state === 'stale' ? 'Re-enable' : 'Enable' }}</Button>
        <Button v-else-if="state === 'on'" variant="secondary" :disabled="busy" @click="disable">Turn off</Button>
      </template>
    </div>
    <p v-if="error" role="alert" class="mt-2 text-sm text-red-700">{{ error }}</p>
    <div v-if="devices.length" class="mt-4">
      <h4 class="text-sm font-medium mb-2">Devices receiving notifications</h4>
      <ul class="divide-y border border-gmail-border rounded-lg">
        <li v-for="device in devices" :key="device.uuid" class="flex items-center gap-3 px-3 py-2 text-sm">
          <span class="flex-1 min-w-0 truncate">{{ device.deviceName || 'Unnamed device' }}<span v-if="device.endpoint === thisDevice" class="text-gmail-gray"> (this device)</span></span>
          <label class="flex items-center gap-2 text-xs text-gmail-gray"><input type="checkbox" :checked="device.notifyNewEmail" @change="toggleNewMail(device)" class="w-4 h-4" />New mail</label>
          <button type="button" class="p-1 rounded hover:bg-red-50" :disabled="busy" :aria-label="`Remove ${device.deviceName || 'device'}`" @click="removeDevice(device)"><Trash2 class="w-4 h-4 text-red-600" /></button>
        </li>
      </ul>
    </div>
  </section>
</template>
