import { Emails } from './resources/emails';
import { Templates } from './resources/templates';
import { Webhooks } from './resources/webhooks';
import { Inbox, Compose, Domains, Identities, Triggers, Deliveries } from './resources/core';
import { MailatError } from './types';
import type { MailatConfig, WebhookPayload } from './types';
export * from './types';
export * from './resources/core';

/**
 * Normalise an instance origin or API root to exactly one `/api/v1` suffix:
 * `https://x`, `https://x/`, `https://x/api/v1` and `https://x/api/v1/` all
 * become `https://x/api/v1`. There is no default host; Mailat is self-hosted.
 */
export function normalizeBaseUrl(baseUrl: string | undefined): string {
  const raw = (baseUrl ?? '').trim();
  if (!raw) throw new Error('baseUrl is required (e.g. https://mail.example.com)');
  let url: URL;
  try { url = new URL(raw); } catch { throw new Error(`baseUrl is not a valid URL: ${raw}`); }
  if ((url.protocol !== 'https:' && url.protocol !== 'http:') || !url.hostname || url.search || url.hash || url.username || url.password) {
    throw new Error(`baseUrl must be an http(s) origin or API root without query, fragment or credentials: ${raw}`);
  }
  const path = url.pathname.replace(/\/+$/, '').replace(/\/api\/v1$/, '');
  return `${url.origin}${path}/api/v1`;
}

export class Mailat {
  private readonly apiKey: string;
  private readonly baseUrl: string;
  private readonly timeout: number;
  readonly emails: Emails;
  readonly templates: Templates;
  readonly webhooks: Webhooks;
  readonly inbox: Inbox;
  readonly compose: Compose;
  readonly domains: Domains;
  readonly identities: Identities;
  readonly triggers: Triggers;
  readonly deliveries: Deliveries;
  constructor(config: MailatConfig) {
    if (!config.apiKey) throw new Error('API key is required');
    this.apiKey = config.apiKey;
    this.baseUrl = normalizeBaseUrl(config.baseUrl);
    this.timeout = config.timeout ?? 30000;
    const request = this.request.bind(this);
    this.emails = new Emails(request); this.templates = new Templates(request); this.webhooks = new Webhooks(request);
    this.inbox = new Inbox(request, path => this.request<Uint8Array>('GET', path, undefined, undefined, true));
    this.compose = new Compose(request); this.domains = new Domains(request); this.identities = new Identities(request);
    this.triggers = new Triggers(request); this.deliveries = new Deliveries(request);
  }
  // No automatic retries: callers retain their original send key and payload,
  // and decide how to reconcile an ambiguous provider/network outcome.
  private async request<T>(method: string, path: string, body?: unknown, headers?: Record<string, string>, binary = false): Promise<T> {
    const controller = new AbortController(); const timeout = setTimeout(() => controller.abort(), this.timeout);
    try {
      const response = await fetch(this.baseUrl + path, { method, headers: { Authorization: `Bearer ${this.apiKey}`, 'Content-Type': 'application/json', ...headers }, body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal });
      if (response.ok && binary) return new Uint8Array(await response.arrayBuffer()) as T;
      const text = await response.text(); let data: any;
      try { data = text ? JSON.parse(text) : {}; } catch { throw new MailatError('Unexpected non-JSON API response', response.status); }
      if (!response.ok) throw new MailatError(data.message || 'Request failed', response.status, data.code, response.headers.get('Retry-After') ?? undefined);
      return data as T;
    } catch (error) {
      if (error instanceof MailatError) throw error;
      if (error instanceof Error) throw new MailatError(error.name === 'AbortError' ? 'Request timeout; keep the same send key when retrying' : error.message, error.name === 'AbortError' ? 408 : 0);
      throw new MailatError('Request failed', 0);
    } finally { clearTimeout(timeout); }
  }

  /** Verify the exact raw bytes, before parsing JSON. Requires await and Web Crypto (Node20+/browsers). */
  static async verifyWebhookSignature(payload: string | Uint8Array, signature: string, secret: string, tolerance = 300): Promise<boolean> {
    if (!secret || !Number.isFinite(tolerance) || tolerance < 0) return false;
    const fields = new Map<string, string>();
    for (const part of signature.split(',')) {
      const match = /^(t|v1)=([^=]+)$/.exec(part.trim());
      if (!match || fields.has(match[1])) return false;
      fields.set(match[1], match[2]);
    }
    const timestamp = fields.get('t') ?? ''; const digest = fields.get('v1') ?? '';
    if (!/^[0-9]{1,13}$/.test(timestamp) || !/^[0-9a-f]{64}$/.test(digest)) return false;
    const seconds = Number(timestamp);
    if (!Number.isSafeInteger(seconds) || seconds <= 0 || Math.abs(Date.now()/1000-seconds) > tolerance) return false;
    const encoder = new TextEncoder(); const raw = typeof payload === 'string' ? encoder.encode(payload) : payload;
    const prefix = encoder.encode(timestamp + '.'); const signed = new Uint8Array(prefix.length + raw.length); signed.set(prefix); signed.set(raw, prefix.length);
    const bytes = Uint8Array.from(digest.match(/../g)!, hex => parseInt(hex, 16));
    const key = await globalThis.crypto.subtle.importKey('raw', encoder.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['verify']);
    // Native cryptographic verification avoids an application-level timing-sensitive comparison.
    return globalThis.crypto.subtle.verify('HMAC', key, bytes, signed);
  }

  /** Optional claimEvent must atomically persist/deduplicate event IDs across workers. */
  static async parseWebhookPayload(payload: string | Uint8Array, signature: string, secret: string, claimEvent?: (id: string) => Promise<boolean>): Promise<WebhookPayload> {
    if (!await Mailat.verifyWebhookSignature(payload, signature, secret)) throw new MailatError('Invalid webhook signature', 401);
    let event: WebhookPayload;
    try { event = JSON.parse(typeof payload === 'string' ? payload : new TextDecoder('utf-8', { fatal: true }).decode(payload)); } catch { throw new MailatError('Invalid webhook JSON', 400); }
    if (!event || event.version !== '1' || typeof event.id !== 'string' || !event.id || typeof event.type !== 'string' || !Number.isFinite(Date.parse(event.createdAt)) || !event.data || typeof event.data !== 'object' || Array.isArray(event.data)) throw new MailatError('Invalid webhook event envelope', 400);
    if (claimEvent && !await claimEvent(event.id)) throw new MailatError('Webhook event already processed', 409);
    return event;
  }
}
export default Mailat;
