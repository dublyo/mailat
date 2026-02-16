/**
 * mailat.co JavaScript/TypeScript SDK
 * Official SDK for interacting with the mailat.co API
 */

export * from './client'
export * from './types'
export * from './resources/emails'
export * from './resources/contacts'
export * from './resources/campaigns'
export * from './resources/domains'
export * from './resources/webhooks'
export * from './resources/templates'

// Re-export main client as default
import { Mailat } from './client'
export default Mailat
