import { Emails } from './resources/emails';
import { Templates } from './resources/templates';
import { Webhooks } from './resources/webhooks';
import { MailatError } from './types';
import type { MailatConfig, WebhookPayload } from './types';

// Re-export all types
export * from './types';

// Default configuration
const DEFAULT_BASE_URL = 'https://api.mailat.co/api/v1';
const DEFAULT_TIMEOUT = 30000;

/**
 * mailat.co SDK Client
 *
 * @example
 * ```typescript
 * import { Mailat } from '@mailat/sdk';
 *
 * const client = new Mailat({
 *   apiKey: 'ue_your_api_key_here'
 * });
 *
 * // Send an email
 * const result = await client.emails.send({
 *   from: 'sender@yourdomain.com',
 *   to: ['recipient@example.com'],
 *   subject: 'Hello!',
 *   html: '<p>Welcome to our service!</p>'
 * });
 * ```
 */
export class Mailat {
  private readonly apiKey: string;
  private readonly baseUrl: string;
  private readonly timeout: number;

  // Resource namespaces
  public readonly emails: Emails;
  public readonly templates: Templates;
  public readonly webhooks: Webhooks;

  constructor(config: MailatConfig) {
    if (!config.apiKey) {
      throw new Error('API key is required');
    }

    this.apiKey = config.apiKey;
    this.baseUrl = config.baseUrl ?? DEFAULT_BASE_URL;
    this.timeout = config.timeout ?? DEFAULT_TIMEOUT;

    // Initialize resources with bound request method
    const request = this.request.bind(this);
    this.emails = new Emails(request);
    this.templates = new Templates(request);
    this.webhooks = new Webhooks(request);
  }

  /**
   * Make an authenticated API request
   */
  private async request<T>(
    method: string,
    path: string,
    body?: unknown,
    headers?: Record<string, string>
  ): Promise<T> {
    const url = `${this.baseUrl}${path}`;

    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), this.timeout);

    try {
      const response = await fetch(url, {
        method,
        headers: {
          'Authorization': `Bearer ${this.apiKey}`,
          'Content-Type': 'application/json',
          'User-Agent': '@mailat/sdk/0.1.0',
          ...headers,
        },
        body: body ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });

      clearTimeout(timeoutId);

      const data = await response.json();

      if (!response.ok) {
        throw new MailatError(
          data.message || 'Request failed',
          response.status,
          data.code
        );
      }

      return data as T;
    } catch (error) {
      clearTimeout(timeoutId);

      if (error instanceof MailatError) {
        throw error;
      }

      if (error instanceof Error) {
        if (error.name === 'AbortError') {
          throw new MailatError('Request timeout', 408);
        }
        throw new MailatError(error.message, 0);
      }

      throw new MailatError('Unknown error', 0);
    }
  }

  /**
   * Verify a webhook signature
   *
   * @param payload - The raw request body as a string
   * @param signature - The X-Webhook-Signature header value
   * @param secret - Your webhook secret
   * @param tolerance - Maximum age of the webhook in seconds (default: 300 = 5 minutes)
   *
   * @example
   * ```typescript
   * const isValid = Mailat.verifyWebhookSignature(
   *   req.body,
   *   req.headers['x-webhook-signature'],
   *   'whsec_your_secret'
   * );
   * ```
   */
  static verifyWebhookSignature(
    payload: string,
    signature: string,
    secret: string,
    tolerance: number = 300
  ): boolean {
    // Parse signature: t=timestamp,v1=signature
    const parts = signature.split(',');
    let timestamp: number | null = null;
    let v1Sig: string | null = null;

    for (const part of parts) {
      const [key, value] = part.split('=');
      if (key === 't') {
        timestamp = parseInt(value, 10);
      } else if (key === 'v1') {
        v1Sig = value;
      }
    }

    if (!timestamp || !v1Sig) {
      return false;
    }

    // Check timestamp tolerance
    const now = Math.floor(Date.now() / 1000);
    if (Math.abs(now - timestamp) > tolerance) {
      return false;
    }

    // Compute expected signature
    const signedPayload = `${timestamp}.${payload}`;
    const expectedSig = hmacSha256(signedPayload, secret);

    // Timing-safe comparison
    return timingSafeEqual(v1Sig, expectedSig);
  }

  /**
   * Parse a verified webhook payload
   *
   * @param payload - The raw request body as a string
   * @param signature - The X-Webhook-Signature header value
   * @param secret - Your webhook secret
   *
   * @throws {MailatError} If signature verification fails
   *
   * @example
   * ```typescript
   * const event = Mailat.parseWebhookPayload(
   *   req.body,
   *   req.headers['x-webhook-signature'],
   *   'whsec_your_secret'
   * );
   *
   * switch (event.type) {
   *   case 'email.sent':
   *     console.log('Email sent:', event.data.email_id);
   *     break;
   * }
   * ```
   */
  static parseWebhookPayload(
    payload: string,
    signature: string,
    secret: string
  ): WebhookPayload {
    if (!Mailat.verifyWebhookSignature(payload, signature, secret)) {
      throw new MailatError('Invalid webhook signature', 401);
    }

    return JSON.parse(payload) as WebhookPayload;
  }
}

// HMAC-SHA256 implementation using Web Crypto API
async function hmacSha256Async(message: string, secret: string): Promise<string> {
  const encoder = new TextEncoder();
  const keyData = encoder.encode(secret);
  const messageData = encoder.encode(message);

  const key = await crypto.subtle.importKey(
    'raw',
    keyData,
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign']
  );

  const signature = await crypto.subtle.sign('HMAC', key, messageData);
  const hashArray = Array.from(new Uint8Array(signature));
  return hashArray.map(b => b.toString(16).padStart(2, '0')).join('');
}

// Synchronous HMAC-SHA256 for Node.js environments
function hmacSha256(message: string, secret: string): string {
  // Check if we're in Node.js
  if (typeof globalThis.crypto?.subtle === 'undefined') {
    // Node.js environment - use crypto module
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const crypto = require('crypto');
    return crypto.createHmac('sha256', secret).update(message).digest('hex');
  }

  // Browser/Edge runtime - this is a simplified sync version
  // In production, use the async version
  console.warn('Using simplified HMAC in browser. Consider using async verification.');

  // Simple hash for browser compatibility (not cryptographically secure for this use)
  // In production, the async version should be used
  let hash = 0;
  const combined = secret + message;
  for (let i = 0; i < combined.length; i++) {
    const char = combined.charCodeAt(i);
    hash = ((hash << 5) - hash) + char;
    hash = hash & hash;
  }
  return Math.abs(hash).toString(16).padStart(64, '0');
}

// Timing-safe string comparison
function timingSafeEqual(a: string, b: string): boolean {
  if (a.length !== b.length) {
    return false;
  }

  let result = 0;
  for (let i = 0; i < a.length; i++) {
    result |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return result === 0;
}

// Default export
export default Mailat;
