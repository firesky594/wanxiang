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

export interface Project {
  id: number
  slug: string
  dir: string
  status: string
  main_commit?: string
  remote_url: string
  created_at: string
}

export interface TaskStep {
  id: number
  task_id: number
  agent_name: string
  kind: string
  status: string
  input: string
  output: string
  created_at: string
  completed_at?: string
}

export interface WorkflowEdge {
  id: number
  task_id: number
  from_step_id?: number
  to_step_id?: number
  label: string
  created_at: string
}

export interface TaskDetail {
  task: Task
  project: Project
  steps: TaskStep[]
  edges: WorkflowEdge[]
}

export async function createAdminTask(title: string, description: string, projectID?: number): Promise<Task> {
  const body: { title: string; description: string; project_id?: number } = { title, description }
  if (projectID !== undefined) body.project_id = projectID
  const response = await api<{ ok: boolean; task: Task }>('/api/admin/tasks', { method: 'POST', body: JSON.stringify(body) })
  return response.task
}

export interface WorkspaceItem {
  id: number
  step_id: number
  assignment_id: number
  agent_name: string
  reports_to?: string
  branch_name: string
  worktree_path: string
  base_commit: string
  provision_commit: string
  write_scope: string[]
  metadata_hash: string
  status: string
  last_error?: string
}

export interface TaskWorkspace {
  task_id: number
  project_id: number
  project_slug: string
  status: string
  items: WorkspaceItem[]
}

async function workspaceAction(taskID: number, action: string, body?: unknown): Promise<TaskWorkspace> {
  const response = await api<{ ok: boolean; workspace: TaskWorkspace }>(`/api/admin/tasks/${taskID}/workspace/${action}`, {
    method: 'POST', body: body === undefined ? undefined : JSON.stringify(body)
  })
  return response.workspace
}
export async function getTaskWorkspace(taskID: number): Promise<TaskWorkspace> { const response = await api<{ok:boolean;workspace:TaskWorkspace}>(`/api/admin/tasks/${taskID}/workspace`); return response.workspace }
export function reconcileTaskWorkspace(taskID: number) { return workspaceAction(taskID, 'reconcile') }
export function repairTaskWorkspace(taskID: number, direction: 'database'|'git_snapshot') { return workspaceAction(taskID, 'repair', { direction }) }
export function cleanupTaskWorkspace(taskID: number, action: 'request'|'confirm', confirmed = false) { return workspaceAction(taskID, 'cleanup', { action, confirmed }) }

export interface MatchRejection {
  name: string
  reasons: string[]
}

export interface MatchDecision {
  id: number
  step_id: number
  selected_agent?: string
  score: number
  reasons: string[]
  rejections: MatchRejection[]
  status: string
}

export interface TaskMatch {
  task_id: number
  decisions: MatchDecision[]
  assignments: Array<{ step_id: number; agent_name: string; reports_to?: string }>
  requires_lead: boolean
  project_lead?: string
  lead_reason?: string
}

export async function getTaskMatch(taskID: number): Promise<TaskMatch> {
  const response = await api<{ ok: boolean; match: TaskMatch }>(`/api/admin/tasks/${taskID}/match`)
  return response.match
}

export async function overrideTaskMatch(taskID: number, stepID: number, agentName: string): Promise<TaskMatch> {
  const response = await api<{ ok: boolean; match: TaskMatch }>(`/api/admin/tasks/${taskID}/match`, {
    method: 'PUT',
    body: JSON.stringify({ step_id: stepID, agent_name: agentName })
  })
  return response.match
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
