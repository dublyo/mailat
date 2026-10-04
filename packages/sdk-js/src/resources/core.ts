import type { ApiResponse, CreateDomainRequest, CreateIdentityRequest, DomainDetails, Identity, InboxCounts, WebhookTestResult } from '../types';
export type JsonObject = Record<string, any>;
type Request = <T>(method: string, path: string, body?: unknown, headers?: Record<string, string>) => Promise<T>;
const id = encodeURIComponent;
function query(values: JsonObject = {}): string { const q = new URLSearchParams(); for (const [k,v] of Object.entries(values)) if (v !== undefined && v !== null) q.set(k, String(v)); return q.size ? '?' + q.toString() : ''; }
class ListResource {
  constructor(protected request: Request, protected path: string) {}
  async list(options?: JsonObject): Promise<any> { return (await this.request<ApiResponse<any>>('GET', this.path + query(options))).data ?? []; }
  async get(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', `${this.path}/${id(uuid)}`)).data; }
}
class Resource extends ListResource {
  async create(body: JsonObject): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', this.path, body)).data; }
  async update(uuid: string, body: JsonObject): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('PUT', `${this.path}/${id(uuid)}`, body)).data; }
  async delete(uuid: string): Promise<void> { await this.request('DELETE', `${this.path}/${id(uuid)}`); }
}
class Labels {
  private resource: Resource;
  constructor(request: Request) { this.resource = new Resource(request, '/inbox/labels'); }
  list() { return this.resource.list(); }
  create(body: JsonObject) { return this.resource.create(body); }
  update(uuid: string, body: JsonObject) { return this.resource.update(uuid, body); }
  delete(uuid: string) { return this.resource.delete(uuid); }
}
export class Inbox {
  readonly labels: Labels; readonly filters: Resource;
  constructor(private request: Request, private download: (path: string) => Promise<Uint8Array>) { this.labels = new Labels(request); this.filters = new Resource(request, '/inbox/filters'); }
  async list(options?: JsonObject): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', '/inbox/received' + query(options))).data; }
  async get(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', '/inbox/received/' + id(uuid))).data; }
  async counts(): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', '/inbox/received/counts')).data; }
  /** Typed alternative to counts(); zero/omitted identityId means the unified mailbox. */
  async folderCounts(identityId?: number): Promise<InboxCounts> { return (await this.request<ApiResponse<InboxCounts>>('GET', '/inbox/received/counts' + query({identityId}))).data; }
  async setupReceiving(domainId: number): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', '/inbox/setup', {domainId})).data; }
  async changes(cursor?: string, limit = 100): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', '/inbox/changes' + query({ cursor, limit }))).data; }
  async attachment(messageUuid: string, attachmentUuid: string): Promise<Uint8Array> { return this.download(`/inbox/received/${id(messageUuid)}/attachments/${id(attachmentUuid)}`); }
  async mark(emailUuids: string[], isRead: boolean): Promise<void> { await this.request('POST', '/inbox/received/mark', { emailUuids, isRead }); }
  async star(emailUuids: string[], isStarred: boolean): Promise<void> { await this.request('POST', '/inbox/received/star', { emailUuids, isStarred }); }
  async move(emailUuids: string[], folder: string): Promise<void> { await this.request('POST', '/inbox/received/move', { emailUuids, folder }); }
  async trash(emailUuids: string[], permanent = false): Promise<void> { await this.request('POST', '/inbox/received/trash', { emailUuids, permanent }); }
  async assignLabels(emailUuids: string[], addLabels: string[], removeLabels: string[] = []): Promise<void> { await this.request('POST', '/inbox/received/labels', { emailUuids, addLabels, removeLabels }); }
  async testFilter(uuid: string, body: JsonObject): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', `/inbox/filters/${id(uuid)}/test`, body)).data; }
}
export class Compose {
  constructor(private request: Request) {}
  async replyContext(uuid: string, replyAll = false): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', '/compose/reply/' + id(uuid) + query({ replyAll }))).data; }
  async forwardContext(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', '/compose/forward/' + id(uuid))).data; }
  async send(body: JsonObject, idempotencyKey: string): Promise<JsonObject> { if (!idempotencyKey || idempotencyKey.length < 8 || idempotencyKey.length > 128) throw new Error('A stable idempotency key is required'); return (await this.request<ApiResponse<JsonObject>>('POST', '/compose/send', body, { 'Idempotency-Key': idempotencyKey })).data; }
  async saveDraft(body: JsonObject): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', '/compose/drafts', body)).data; }
  async updateDraft(uuid: string, body: JsonObject): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('PUT', '/compose/drafts/' + id(uuid), body)).data; }
  async deleteDraft(uuid: string): Promise<void> { await this.request('DELETE', '/compose/drafts/' + id(uuid)); }
}
export class Domains extends ListResource {
  constructor(request: Request) { super(request, '/domains'); }
  async create(body: CreateDomainRequest): Promise<DomainDetails> { return (await this.request<ApiResponse<DomainDetails>>('POST', this.path, body)).data; }
  async delete(uuid: string): Promise<void> { await this.request('DELETE', `${this.path}/${id(uuid)}`); }
  async verify(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', `/domains/${id(uuid)}/verify`)).data; }
  async initiateSES(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', `/domains/${id(uuid)}/ses-verify`)).data; }
  async sesStatus(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', `/domains/${id(uuid)}/ses-status`)).data; }
  async dmarc(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', `/domains/${id(uuid)}/dmarc`)).data; }
  async addCloudflareDNS(uuid: string, body: JsonObject): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', `/domains/${id(uuid)}/dns/cloudflare`,body)).data; }
  async setupSending(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', `/domains/${id(uuid)}/setup-sending`)).data; }
  async sendingStatus(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('GET', `/domains/${id(uuid)}/sending-status`)).data; }
}
export class Identities extends Resource {
  constructor(request: Request) { super(request, '/identities'); }
  async create(body: CreateIdentityRequest): Promise<Identity> { return (await this.request<ApiResponse<Identity>>('POST', this.path, body)).data; }
}
export class Triggers extends Resource {
  constructor(request: Request) { super(request, '/webhook-triggers'); }
  async test(uuid: string): Promise<WebhookTestResult> { return (await this.request<ApiResponse<WebhookTestResult>>('POST', `${this.path}/${id(uuid)}/test`)).data; }
  async rotateSecret(uuid: string): Promise<string> { return (await this.request<ApiResponse<{secret:string}>>('POST', `${this.path}/${id(uuid)}/rotate-secret`)).data.secret; }
}
export class Deliveries extends ListResource {
  constructor(request: Request) { super(request, '/webhook-deliveries'); }
  async replay(uuid: string): Promise<JsonObject> { return (await this.request<ApiResponse<JsonObject>>('POST', `${this.path}/${id(uuid)}/replay`)).data; }
}
