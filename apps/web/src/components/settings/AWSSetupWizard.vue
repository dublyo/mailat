<script setup lang="ts">
import { ref, computed } from 'vue'
import { Cloud, Key, CheckCircle, XCircle, Loader2, Server, Database, Bell, ArrowRight, Copy, ExternalLink } from 'lucide-vue-next'
import Button from '@/components/common/Button.vue'
import Badge from '@/components/common/Badge.vue'
import { api } from '@/lib/api'

const emit = defineEmits<{
  (e: 'complete'): void
  (e: 'close'): void
}>()

// Form state
const currentStep = ref(1)
const isLoading = ref(false)
const error = ref('')

const awsCredentials = ref({
  region: 'us-east-1',
  accessKeyId: '',
  secretAccessKey: ''
})

interface ProvisioningResult {
  success: boolean
  resources?: {
    s3BucketName: string
    lambdaFunctionArn: string
    snsTopicArn: string
    receiptRuleSetName: string
    region: string
  }
  nextSteps?: string[]
  error?: string
}

const validationResult = ref<{ valid: boolean; message: string } | null>(null)
const provisioningResult = ref<ProvisioningResult | null>(null)

const awsRegions = [
  { value: 'us-east-1', label: 'US East (N. Virginia)' },
  { value: 'us-east-2', label: 'US East (Ohio)' },
  { value: 'us-west-1', label: 'US West (N. California)' },
  { value: 'us-west-2', label: 'US West (Oregon)' },
  { value: 'eu-west-1', label: 'EU (Ireland)' },
  { value: 'eu-west-2', label: 'EU (London)' },
  { value: 'eu-central-1', label: 'EU (Frankfurt)' },
  { value: 'ap-southeast-1', label: 'Asia Pacific (Singapore)' },
  { value: 'ap-southeast-2', label: 'Asia Pacific (Sydney)' },
  { value: 'ap-northeast-1', label: 'Asia Pacific (Tokyo)' },
]

const canProceedStep1 = computed(() =>
  awsCredentials.value.accessKeyId.length > 10 &&
  awsCredentials.value.secretAccessKey.length > 10
)

const canProceedStep2 = computed(() => validationResult.value?.valid === true)

// Validate credentials
async function validateCredentials() {
  isLoading.value = true
  error.value = ''
  validationResult.value = null

  try {
    const result = await api.post<{ valid: boolean; message: string; error?: string }>(
      '/api/v1/settings/aws/validate',
      awsCredentials.value
    )
    validationResult.value = result
    if (result.valid) {
      currentStep.value = 2
    }
  } catch (e: any) {
    error.value = e.message || 'Failed to validate credentials'
    validationResult.value = { valid: false, message: error.value }
  } finally {
    isLoading.value = false
  }
}

// Provision resources
async function provisionResources() {
  isLoading.value = true
  error.value = ''
  provisioningResult.value = null

  try {
    const result = await api.post<ProvisioningResult>(
      '/api/v1/settings/aws/provision',
      awsCredentials.value
    )
    provisioningResult.value = result
    if (result?.success) {
      currentStep.value = 3
    }
  } catch (e: any) {
    error.value = e.message || 'Failed to provision resources'
    provisioningResult.value = { success: false, error: error.value }
  } finally {
    isLoading.value = false
  }
}

function copyToClipboard(text: string) {
  navigator.clipboard.writeText(text)
}

function goToStep(step: number) {
  if (step <= currentStep.value) {
    currentStep.value = step
  }
}
</script>

<template>
  <div class="bg-white rounded-lg border border-gmail-border">
    <!-- Header -->
    <div class="p-6 border-b border-gmail-border">
      <div class="flex items-center gap-3">
        <div class="w-10 h-10 rounded-lg bg-orange-100 flex items-center justify-center">
          <Cloud class="w-5 h-5 text-orange-600" />
        </div>
        <div>
          <h2 class="text-lg font-medium">AWS SES Setup</h2>
          <p class="text-sm text-gmail-gray">Configure AWS to receive emails for all your domains</p>
        </div>
      </div>
    </div>

    <!-- Progress Steps -->
    <div class="px-6 py-4 bg-gray-50 border-b border-gmail-border">
      <div class="flex items-center justify-between">
        <button
          v-for="(step, index) in ['Credentials', 'Provision', 'Complete']"
          :key="step"
          @click="goToStep(index + 1)"
          class="flex items-center gap-2"
          :class="{ 'opacity-50': index + 1 > currentStep }"
        >
          <div
            :class="[
              'w-8 h-8 rounded-full flex items-center justify-center text-sm font-medium',
              currentStep > index + 1 ? 'bg-green-500 text-white' :
              currentStep === index + 1 ? 'bg-gmail-blue text-white' :
              'bg-gray-200 text-gray-500'
            ]"
          >
            <CheckCircle v-if="currentStep > index + 1" class="w-5 h-5" />
            <span v-else>{{ index + 1 }}</span>
          </div>
          <span class="text-sm font-medium hidden sm:inline">{{ step }}</span>
          <ArrowRight v-if="index < 2" class="w-4 h-4 text-gray-300 mx-2" />
        </button>
      </div>
    </div>

    <!-- Step Content -->
    <div class="p-6">
      <!-- Step 1: Enter Credentials -->
      <div v-if="currentStep === 1" class="space-y-6">
        <div class="bg-blue-50 border border-blue-200 rounded-lg p-4">
          <h3 class="font-medium text-blue-800 mb-2">What you'll need:</h3>
          <ul class="text-sm text-blue-700 space-y-1">
            <li>• AWS Access Key ID and Secret Access Key</li>
            <li>• IAM user with permissions for: SES, S3, Lambda, IAM, SNS</li>
            <li>• SES should be out of sandbox mode (or request production access)</li>
          </ul>
        </div>

        <div class="space-y-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">AWS Region</label>
            <select
              v-model="awsCredentials.region"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:ring-2 focus:ring-gmail-blue"
            >
              <option v-for="region in awsRegions" :key="region.value" :value="region.value">
                {{ region.label }}
              </option>
            </select>
          </div>

          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">Access Key ID</label>
            <input
              v-model="awsCredentials.accessKeyId"
              type="text"
              placeholder="AKIAIOSFODNN7EXAMPLE"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:ring-2 focus:ring-gmail-blue font-mono"
            />
          </div>

          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">Secret Access Key</label>
            <input
              v-model="awsCredentials.secretAccessKey"
              type="password"
              placeholder="wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
              class="w-full px-3 py-2 border border-gmail-border rounded-lg focus:outline-none focus:ring-2 focus:ring-gmail-blue font-mono"
            />
          </div>
        </div>

        <!-- Validation Result -->
        <div v-if="validationResult" :class="[
          'p-4 rounded-lg flex items-start gap-3',
          validationResult.valid ? 'bg-green-50 border border-green-200' : 'bg-red-50 border border-red-200'
        ]">
          <component
            :is="validationResult.valid ? CheckCircle : XCircle"
            :class="['w-5 h-5 mt-0.5', validationResult.valid ? 'text-green-600' : 'text-red-600']"
          />
          <div>
            <p :class="validationResult.valid ? 'text-green-800' : 'text-red-800'">
              {{ validationResult.message }}
            </p>
          </div>
        </div>

        <div class="flex justify-end gap-3">
          <Button variant="secondary" @click="emit('close')">Cancel</Button>
          <Button
            @click="validateCredentials"
            :disabled="!canProceedStep1 || isLoading"
          >
            <Loader2 v-if="isLoading" class="w-4 h-4 animate-spin" />
            <Key v-else class="w-4 h-4" />
            Validate Credentials
          </Button>
        </div>
      </div>

      <!-- Step 2: Provision Resources -->
      <div v-else-if="currentStep === 2" class="space-y-6">
        <div class="bg-amber-50 border border-amber-200 rounded-lg p-4">
          <h3 class="font-medium text-amber-800 mb-2">Resources to be created:</h3>
          <div class="grid grid-cols-2 gap-4 mt-3">
            <div class="flex items-center gap-2 text-sm text-amber-700">
              <Database class="w-4 h-4" />
              <span>S3 Bucket (email storage)</span>
            </div>
            <div class="flex items-center gap-2 text-sm text-amber-700">
              <Server class="w-4 h-4" />
              <span>Lambda Function (processor)</span>
            </div>
            <div class="flex items-center gap-2 text-sm text-amber-700">
              <Bell class="w-4 h-4" />
              <span>SNS Topic (notifications)</span>
            </div>
            <div class="flex items-center gap-2 text-sm text-amber-700">
              <Key class="w-4 h-4" />
              <span>IAM Role (permissions)</span>
            </div>
          </div>
        </div>

        <div class="bg-gray-50 rounded-lg p-4">
          <h3 class="font-medium mb-2">Validated Credentials</h3>
          <div class="text-sm text-gray-600 space-y-1">
            <p><span class="font-medium">Region:</span> {{ awsCredentials.region }}</p>
            <p><span class="font-medium">Access Key:</span> {{ awsCredentials.accessKeyId.slice(0, 8) }}...****</p>
          </div>
        </div>

        <!-- Provisioning Error -->
        <div v-if="provisioningResult && !provisioningResult.success" class="bg-red-50 border border-red-200 rounded-lg p-4">
          <div class="flex items-start gap-3">
            <XCircle class="w-5 h-5 text-red-600 mt-0.5" />
            <div>
              <p class="font-medium text-red-800">Provisioning Failed</p>
              <p class="text-sm text-red-700 mt-1">{{ provisioningResult.error }}</p>
            </div>
          </div>
        </div>

        <div class="flex justify-between">
          <Button variant="secondary" @click="currentStep = 1">Back</Button>
          <Button
            @click="provisionResources"
            :disabled="isLoading"
          >
            <Loader2 v-if="isLoading" class="w-4 h-4 animate-spin" />
            <Cloud v-else class="w-4 h-4" />
            Create AWS Resources
          </Button>
        </div>
      </div>

      <!-- Step 3: Complete -->
      <div v-else-if="currentStep === 3" class="space-y-6">
        <div class="bg-green-50 border border-green-200 rounded-lg p-4">
          <div class="flex items-start gap-3">
            <CheckCircle class="w-6 h-6 text-green-600" />
            <div>
              <h3 class="font-medium text-green-800">AWS Resources Created Successfully!</h3>
              <p class="text-sm text-green-700 mt-1">All required resources have been provisioned in your AWS account.</p>
            </div>
          </div>
        </div>

        <!-- Created Resources -->
        <div v-if="provisioningResult?.resources" class="space-y-3">
          <h3 class="font-medium">Created Resources:</h3>
          <div class="bg-gray-50 rounded-lg p-4 space-y-3">
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-2">
                <Database class="w-4 h-4 text-gray-500" />
                <span class="text-sm">S3 Bucket</span>
              </div>
              <div class="flex items-center gap-2">
                <code class="text-xs bg-white px-2 py-1 rounded border">
                  {{ provisioningResult.resources.s3BucketName }}
                </code>
                <button @click="copyToClipboard(provisioningResult.resources.s3BucketName)" class="p-1 hover:bg-gray-200 rounded">
                  <Copy class="w-4 h-4 text-gray-500" />
                </button>
              </div>
            </div>
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-2">
                <Server class="w-4 h-4 text-gray-500" />
                <span class="text-sm">Lambda Function</span>
              </div>
              <Badge variant="success">Active</Badge>
            </div>
            <div class="flex items-center justify-between">
              <div class="flex items-center gap-2">
                <Bell class="w-4 h-4 text-gray-500" />
                <span class="text-sm">SNS Topic</span>
              </div>
              <Badge variant="success">Created</Badge>
            </div>
          </div>
        </div>

        <!-- Next Steps -->
        <div class="bg-blue-50 border border-blue-200 rounded-lg p-4">
          <h3 class="font-medium text-blue-800 mb-3">Next Steps:</h3>
          <ol class="text-sm text-blue-700 space-y-2 list-decimal list-inside">
            <li>Go to <a href="https://console.aws.amazon.com/ses" target="_blank" class="underline inline-flex items-center gap-1">AWS SES Console <ExternalLink class="w-3 h-3" /></a></li>
            <li>Navigate to "Email Receiving" → "Rule Sets"</li>
            <li>Activate the rule set: <code class="bg-blue-100 px-1 rounded">{{ provisioningResult?.resources?.receiptRuleSetName }}</code></li>
            <li>Add your domains to the receipt rules</li>
            <li>Update your domain's MX records to point to SES</li>
          </ol>
        </div>

        <div class="flex justify-end gap-3">
          <Button @click="emit('complete')">
            <CheckCircle class="w-4 h-4" />
            Done
          </Button>
        </div>
      </div>
    </div>
  </div>
</template>
