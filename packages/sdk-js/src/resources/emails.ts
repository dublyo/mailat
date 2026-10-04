import type {
  SendEmailRequest,
  SendEmailResponse,
  BatchSendRequest,
  BatchSendResponse,
  EmailStatusResponse,
  ApiResponse,
} from '../types';

export class Emails {
  constructor(private request: <T>(method: string, path: string, body?: unknown, headers?: Record<string, string>) => Promise<T>) {}

  /**
   * Send a single transactional email
   * @param data - Email data including recipients, subject, and content
   * @param options - Optional settings like idempotency key
   */
  async send(
    data: SendEmailRequest,
    options?: { idempotencyKey?: string }
  ): Promise<SendEmailResponse> {
    const key = options?.idempotencyKey ?? data.idempotencyKey;
    if (!key || key.length < 8 || key.length > 128) throw new Error('An 8–128 character idempotency key is required');
    if (options?.idempotencyKey && data.idempotencyKey && options.idempotencyKey !== data.idempotencyKey) throw new Error('Idempotency keys must match');
    const headers: Record<string, string> = { 'Idempotency-Key': key };
    if (options?.idempotencyKey) {
      headers['Idempotency-Key'] = options.idempotencyKey;
    }

    const response = await this.request<ApiResponse<SendEmailResponse>>(
      'POST',
      '/emails',
      data,
      headers
    );
    return response.data;
  }

  /**
   * Send multiple emails in a single batch request (up to 100)
   * @param emails - Array of email requests
   */
  async sendBatch(emails: SendEmailRequest[], options?: { idempotencyKey: string }): Promise<BatchSendResponse> {
    if (!options?.idempotencyKey || options.idempotencyKey.length < 8 || options.idempotencyKey.length > 128) throw new Error('A stable batch idempotency key is required');
    if (emails.length === 0 || emails.length > 100) {
      throw new Error('Batch size cannot exceed 100 emails');
    }

    const response = await this.request<ApiResponse<BatchSendResponse>>(
      'POST',
      '/emails/batch',
      { emails } as BatchSendRequest,
      { 'Idempotency-Key': options.idempotencyKey }
    );
    return response.data;
  }

  /**
   * Get the status and delivery events for an email
   * @param id - The email UUID
   */
  async get(id: string): Promise<EmailStatusResponse> {
    const response = await this.request<ApiResponse<EmailStatusResponse>>(
      'GET',
      `/emails/${encodeURIComponent(id)}`
    );
    return response.data;
  }

  /**
   * Cancel a scheduled email (only works for emails in 'queued' status)
   * @param id - The email UUID
   */
  async cancel(id: string): Promise<void> {
    await this.request<ApiResponse<null>>('DELETE', `/emails/${encodeURIComponent(id)}`);
  }
}
