import type {
  CreateWebhookRequest,
  UpdateWebhookRequest,
  Webhook,
  WebhookCall,
  RotateSecretResponse,
  ApiResponse,
} from '../types';

export class Webhooks {
  constructor(private request: <T>(method: string, path: string, body?: unknown) => Promise<T>) {}

  /**
   * Create a new webhook endpoint
   * @param data - Webhook data including URL and events to subscribe to
   */
  async create(data: CreateWebhookRequest): Promise<Webhook> {
    const response = await this.request<ApiResponse<Webhook>>(
      'POST',
      '/webhooks',
      data
    );
    return response.data;
  }

  /**
   * Get a webhook by UUID
   * @param uuid - The webhook UUID
   */
  async get(uuid: string): Promise<Webhook> {
    const response = await this.request<ApiResponse<Webhook>>(
      'GET',
      `/webhooks/${uuid}`
    );
    return response.data;
  }

  /**
   * List all webhooks for the organization
   */
  async list(): Promise<Webhook[]> {
    const response = await this.request<ApiResponse<Webhook[]>>(
      'GET',
      '/webhooks'
    );
    return response.data;
  }

  /**
   * Update a webhook
   * @param uuid - The webhook UUID
   * @param data - Fields to update
   */
  async update(uuid: string, data: UpdateWebhookRequest): Promise<Webhook> {
    const response = await this.request<ApiResponse<Webhook>>(
      'PUT',
      `/webhooks/${uuid}`,
      data
    );
    return response.data;
  }

  /**
   * Delete a webhook
   * @param uuid - The webhook UUID
   */
  async delete(uuid: string): Promise<void> {
    await this.request<ApiResponse<null>>('DELETE', `/webhooks/${uuid}`);
  }

  /**
   * Rotate the webhook secret
   * @param uuid - The webhook UUID
   */
  async rotateSecret(uuid: string): Promise<string> {
    const response = await this.request<ApiResponse<RotateSecretResponse>>(
      'POST',
      `/webhooks/${uuid}/rotate-secret`
    );
    return response.data.secret;
  }

  /**
   * Get recent webhook delivery attempts
   * @param uuid - The webhook UUID
   * @param limit - Maximum number of calls to return (default: 50)
   */
  async getCalls(uuid: string, limit?: number): Promise<WebhookCall[]> {
    const query = limit ? `?limit=${limit}` : '';
    const response = await this.request<ApiResponse<WebhookCall[]>>(
      'GET',
      `/webhooks/${uuid}/calls${query}`
    );
    return response.data;
  }

  /**
   * Send a test webhook event
   * @param uuid - The webhook UUID
   */
  async test(uuid: string): Promise<void> {
    await this.request<ApiResponse<null>>('POST', `/webhooks/${uuid}/test`);
  }
}
