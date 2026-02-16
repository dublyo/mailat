import type {
  CreateTemplateRequest,
  UpdateTemplateRequest,
  Template,
  PreviewTemplateRequest,
  PreviewTemplateResponse,
  ApiResponse,
} from '../types';

export class Templates {
  constructor(private request: <T>(method: string, path: string, body?: unknown) => Promise<T>) {}

  /**
   * Create a new email template
   * @param data - Template data including name, subject, and content
   */
  async create(data: CreateTemplateRequest): Promise<Template> {
    const response = await this.request<ApiResponse<Template>>(
      'POST',
      '/templates',
      data
    );
    return response.data;
  }

  /**
   * Get a template by UUID
   * @param uuid - The template UUID
   */
  async get(uuid: string): Promise<Template> {
    const response = await this.request<ApiResponse<Template>>(
      'GET',
      `/templates/${uuid}`
    );
    return response.data;
  }

  /**
   * List all templates for the organization
   */
  async list(): Promise<Template[]> {
    const response = await this.request<ApiResponse<Template[]>>(
      'GET',
      '/templates'
    );
    return response.data;
  }

  /**
   * Update a template
   * @param uuid - The template UUID
   * @param data - Fields to update
   */
  async update(uuid: string, data: UpdateTemplateRequest): Promise<Template> {
    const response = await this.request<ApiResponse<Template>>(
      'PUT',
      `/templates/${uuid}`,
      data
    );
    return response.data;
  }

  /**
   * Delete a template
   * @param uuid - The template UUID
   */
  async delete(uuid: string): Promise<void> {
    await this.request<ApiResponse<null>>('DELETE', `/templates/${uuid}`);
  }

  /**
   * Preview a template with variables substituted
   * @param uuid - The template UUID
   * @param variables - Variables to substitute in the template
   */
  async preview(
    uuid: string,
    variables?: Record<string, string>
  ): Promise<PreviewTemplateResponse> {
    const response = await this.request<ApiResponse<PreviewTemplateResponse>>(
      'POST',
      `/templates/${uuid}/preview`,
      { variables } as PreviewTemplateRequest
    );
    return response.data;
  }
}
