import { api } from './api'
export interface SignupFormDraft {
  listId: string; identityId: string; name: string; title: string; description: string;
  consentText: string; buttonText: string; privacyUrl: string; collectName: boolean; published: boolean
}
export interface SignupForm extends SignupFormDraft {
  uuid: string; listName: string; fromEmail: string; confirmationMode: 'single' | 'double';
  version: number; subscribed: number; pending: number; createdAt: string
}
export interface PublicForm {
  uuid: string; title: string; description: string; consentText: string; buttonText: string;
  privacyUrl: string; collectName: boolean; confirmationMode: 'single' | 'double'; challenge: string
}
export interface SignupEntries {
  items: { id: string; email: string; firstName: string; status: string; confirmationMode: string; createdAt: string; confirmedAt: string | null }[];
  total: number; page: number; pageSize: number
}
export const signupFormsApi = {
  list: () => api.get<SignupForm[]>('/api/v1/signup-forms'),
  create: (data: SignupFormDraft) => api.post<SignupForm>('/api/v1/signup-forms', data),
  update: (id: string, data: SignupFormDraft) => api.put<SignupForm>(`/api/v1/signup-forms/${id}`, data),
  entries: (id: string, page = 1) => api.get<SignupEntries>(`/api/v1/signup-forms/${id}/signups?page=${page}`),
}
// This client deliberately omits authentication and browser storage so iframe
// submissions cannot expose a logged-in owner's credentials to embedding sites.
export async function publicSignup<T>(path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${import.meta.env.VITE_API_URL || ''}/api/v1/public/forms${path}`, {
    method: body === undefined ? 'GET' : 'POST', credentials: 'omit', cache: 'no-store',
    headers: body === undefined ? {} : { 'Content-Type': 'application/json' },
    ...(body === undefined ? {} : { body: JSON.stringify(body) })
  })
  const payload = await res.json()
  if (!res.ok) throw new Error(payload.message || 'Something went wrong. Please try again.')
  return payload.data as T
}
