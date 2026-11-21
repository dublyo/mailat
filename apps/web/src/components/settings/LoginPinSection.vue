<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { Check } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import Modal from '@/components/common/Modal.vue'
import PinInput from '@/components/common/PinInput.vue'
import { settingsApi, type LoginPinStatus, type TrustedDevice } from '@/lib/api'
import { forgetDevice, pinProblem } from '@/lib/loginPin'
import { useAuthStore } from '@/stores/auth'

// Settings > Security for every role: the login PIN asked after the password,
// and the browsers that skip it for 30 days.
const authStore = useAuthStore()
const status = ref<LoginPinStatus | null>(null)
const devices = ref<TrustedDevice[]>([])
const loadError = ref('')
const notice = ref('')
const busy = ref(false)

const showChange = ref(false)
const currentPassword = ref('')
const newPin = ref('')
const confirmPin = ref('')
const changeError = ref('')

async function load() {
  loadError.value = ''
  try {
    const [s, d] = await Promise.all([settingsApi.pinStatus(), settingsApi.trustedDevices()])
    status.value = s
    devices.value = d || []
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : 'Could not load your login PIN settings.'
  }
}

onMounted(load)

function formatDay(value?: string) {
  return value ? new Date(value).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' }) : ''
}

function formatTime(value: string) {
  return new Date(value).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

// A short, readable name for a user agent string.
function deviceName(ua: string) {
  if (!ua) return 'Unknown browser'
  const browser = /Edg\//.test(ua) ? 'Edge' : /Firefox\//.test(ua) ? 'Firefox' : /Chrome\//.test(ua) ? 'Chrome' : /Safari\//.test(ua) ? 'Safari' : 'Browser'
  const os = /iPhone|iPad/.test(ua) ? 'iOS' : /Android/.test(ua) ? 'Android' : /Mac OS X|Macintosh/.test(ua) ? 'macOS' : /Windows/.test(ua) ? 'Windows' : /Linux/.test(ua) ? 'Linux' : ''
  return os ? `${browser} on ${os}` : browser
}

function openChange() {
  currentPassword.value = ''
  newPin.value = ''
  confirmPin.value = ''
  changeError.value = ''
  showChange.value = true
}

async function submitChange() {
  changeError.value = ''
  const problem = pinProblem(newPin.value)
  if (problem) { changeError.value = problem; return }
  if (newPin.value !== confirmPin.value) { changeError.value = 'The two PINs do not match.'; return }
  if (!currentPassword.value) { changeError.value = 'Enter your current password.'; return }
  busy.value = true
  try {
    status.value = await settingsApi.changePin(currentPassword.value, newPin.value)
    if (authStore.user) forgetDevice(authStore.user.email)
    devices.value = []
    showChange.value = false
    notice.value = 'Login PIN saved. Remembered devices will ask for the new PIN.'
  } catch (e) {
    changeError.value = e instanceof Error ? e.message : 'Could not change the PIN.'
  } finally {
    currentPassword.value = ''
    busy.value = false
  }
}

async function forgetOne(device: TrustedDevice) {
  busy.value = true
  notice.value = ''
  try {
    await settingsApi.forgetTrustedDevice(device.uuid)
    devices.value = devices.value.filter(d => d.uuid !== device.uuid)
    notice.value = 'That device will ask for the PIN at its next sign-in.'
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : 'Could not forget the device.'
  } finally {
    busy.value = false
  }
}

async function forgetAll() {
  if (!confirm('Every remembered device, including this one, will ask for the PIN at its next sign-in. Continue?')) return
  busy.value = true
  notice.value = ''
  try {
    await settingsApi.forgetAllTrustedDevices()
    if (authStore.user) forgetDevice(authStore.user.email)
    devices.value = []
    notice.value = 'All remembered devices were forgotten.'
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : 'Could not forget the devices.'
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <section class="pt-6 border-t border-gmail-border">
    <h3 class="text-sm font-medium text-gmail-gray mb-4">Login PIN</h3>
    <div v-if="loadError" role="alert" class="mb-3 p-3 bg-red-50 text-red-700 rounded-lg text-sm flex gap-3">
      <span class="flex-1">{{ loadError }}</span>
      <button class="underline" @click="load">Retry</button>
    </div>
    <div v-if="notice" role="status" class="mb-3 p-3 bg-green-50 text-green-800 rounded-lg text-sm">{{ notice }}</div>
    <div class="p-4 bg-gmail-lightGray rounded-lg flex items-center justify-between gap-4">
      <div>
        <p class="font-medium">4-digit PIN after your password</p>
        <p class="text-sm text-gmail-gray">
          <template v-if="!status">Checking…</template>
          <template v-else-if="status.set">
            <span class="text-green-700 inline-flex items-center gap-1"><Check class="w-4 h-4" /> Set{{ status.setAt ? ` on ${formatDay(status.setAt)}` : '' }}</span>
          </template>
          <template v-else>Not set yet. You will be asked to choose one at your next sign-in.</template>
        </p>
      </div>
      <Button variant="secondary" :disabled="!status || busy" @click="openChange">{{ status?.set ? 'Change PIN' : 'Set PIN' }}</Button>
    </div>

    <div class="mt-4">
      <div class="flex items-center justify-between mb-2">
        <p class="text-sm font-medium">Remembered devices</p>
        <button v-if="devices.length" class="text-gmail-red text-sm hover:underline disabled:opacity-50" :disabled="busy" @click="forgetAll">Forget all remembered devices</button>
      </div>
      <p class="text-xs text-gmail-gray mb-2">These browsers skip the PIN until the date shown. Changing your password or PIN, or signing out of all sessions, forgets them too.</p>
      <p v-if="!devices.length" class="text-sm text-gmail-gray">No remembered devices.</p>
      <div class="space-y-2">
        <div v-for="device in devices" :key="device.uuid" class="flex items-center justify-between gap-3 p-3 border border-gmail-border rounded-lg">
          <div class="min-w-0">
            <p class="font-medium text-sm truncate" :title="device.userAgent">{{ deviceName(device.userAgent) }}</p>
            <p class="text-xs text-gmail-gray">
              Added {{ formatTime(device.createdAt) }} · Last used {{ formatTime(device.lastUsedAt) }} · Until {{ formatDay(device.expiresAt) }}{{ device.ip ? ` · ${device.ip}` : '' }}
            </p>
          </div>
          <button class="text-gmail-red text-sm hover:underline shrink-0 disabled:opacity-50" :disabled="busy" @click="forgetOne(device)">Forget</button>
        </div>
      </div>
    </div>

    <Modal :open="showChange" :title="status?.set ? 'Change login PIN' : 'Set login PIN'" @close="showChange = false">
      <form class="space-y-4" @submit.prevent="submitChange">
        <label class="block text-sm font-medium text-gmail-gray">Current password
          <input v-model="currentPassword" type="password" autocomplete="current-password" required class="mt-1 w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:border-gmail-blue" />
        </label>
        <div>
          <p class="text-sm font-medium text-gmail-gray mb-2">New PIN</p>
          <PinInput v-model="newPin" label="New PIN" :disabled="busy" :invalid="newPin.length === 4 && !!pinProblem(newPin)" />
          <p v-if="newPin.length === 4 && pinProblem(newPin)" class="mt-2 text-sm text-red-700 text-center">{{ pinProblem(newPin) }}</p>
        </div>
        <div>
          <p class="text-sm font-medium text-gmail-gray mb-2">Enter the new PIN again</p>
          <PinInput v-model="confirmPin" label="Confirm new PIN" :disabled="busy" :invalid="confirmPin.length === 4 && confirmPin !== newPin" />
        </div>
        <p class="text-xs text-gmail-gray">Remembered devices will ask for the new PIN. Your sessions stay signed in.</p>
        <p v-if="changeError" role="alert" class="p-3 bg-red-50 text-red-700 rounded-lg text-sm">{{ changeError }}</p>
        <div class="flex justify-end gap-2">
          <Button type="button" variant="secondary" @click="showChange = false">Cancel</Button>
          <Button type="submit" :disabled="busy" :loading="busy">Save PIN</Button>
        </div>
      </form>
    </Modal>
  </section>
</template>
