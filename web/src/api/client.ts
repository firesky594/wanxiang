export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (!headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json')
  }
  const token = localStorage.getItem('wanxiang_admin_token')
  if (token) {
    headers.set('Authorization', `Bearer ${token}`)
  }
  const res = await fetch(path, {
    ...init,
    credentials: 'same-origin',
    headers
  })
  if (!res.ok) {
    throw new Error(await res.text())
  }
  return res.json() as Promise<T>
}

export interface ManagerStatus {
  status: string
  missing_env: string[]
}

export type ProviderType = 'openai' | 'deepseek'

export interface AgentConfig {
  name: string
  provider_type: ProviderType | ''
  base_url: string
  model: string
  secret_configured: boolean
  status: string
  last_error?: string
}

export interface AgentConfigInput {
  provider_type: ProviderType
  base_url: string
  model: string
  api_key: string
}

export async function listAgentConfigs(): Promise<AgentConfig[]> {
  const res = await api<{ ok: boolean; agents: AgentConfig[] }>('/api/admin/agents')
  return res.agents
}

export async function saveAgentConfig(name: string, input: AgentConfigInput): Promise<AgentConfig> {
  const res = await api<{ ok: boolean; agent: AgentConfig }>(`/api/admin/agents/${encodeURIComponent(name)}/config`, {
    method: 'PUT',
    body: JSON.stringify(input)
  })
  return res.agent
}

export async function probeAgent(name: string): Promise<AgentConfig> {
  const res = await api<{ ok: boolean; agent: AgentConfig }>(`/api/admin/agents/${encodeURIComponent(name)}/probe`, {
    method: 'POST'
  })
  return res.agent
}

export interface Task {
  id: number
  project_id: number
  project_slug: string
  title: string
  description: string
  status: string
}

export interface MergeRequest {
  id: number
  project_id: number
  task_id: number
  title: string
  source_branch: string
  target_branch: string
  status: string
}

export interface Issue {
  id: number
  task_id?: number
  mr_id?: number
  title: string
  body: string
  status: string
  blocking: boolean
  created_by: string
}
