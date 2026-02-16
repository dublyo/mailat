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
    const headers: Record<string, string> = {};
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
  async sendBatch(emails: SendEmailRequest[]): Promise<BatchSendResponse> {
    if (emails.length > 100) {
      throw new Error('Batch size cannot exceed 100 emails');
    }

    const response = await this.request<ApiResponse<BatchSendResponse>>(
      'POST',
      '/emails/batch',
      { emails } as BatchSendRequest
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
      `/emails/${id}`
    );
    return response.data;
  }

  /**
   * Cancel a scheduled email (only works for emails in 'queued' status)
   * @param id - The email UUID
   */
  async cancel(id: string): Promise<void> {
    await this.request<ApiResponse<null>>('DELETE', `/emails/${id}`);
  }
}
