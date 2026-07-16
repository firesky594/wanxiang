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

export interface StepRecovery {
  step_id: number
  agent_name: string
  status: string
  lease_version: number
  checkpoint_id?: number
  attempt: number
  last_heartbeat_at?: string
  lease_expires_at?: string
  resume_deadline?: string
}

export interface TaskLease {
  task_id: number
  step_id: number
  agent_name: string
  lease_id: string
  lease_version: number
  status: string
  expires_at: string
  last_heartbeat_at?: string
  interrupted_at?: string
  resume_deadline?: string
}

export interface TaskCheckpoint {
  id: number
  task_id: number
  step_id: number
  git_commit: string
  branch_name: string
  clean: boolean
  summary_hash: string
  high_risk: boolean
  created_at: string
  summary: { completed: string[]; next_action: string; files_changed?: string[]; decisions?: string[]; blockers?: string[]; risks?: string[] }
}

export interface LeaseTimeline {
  task_id: number
  steps: StepRecovery[]
  leases: TaskLease[]
  checkpoints: TaskCheckpoint[]
  reassignments: Array<{ id: number; step_id: number; from_agent: string; to_agent: string; checkpoint_id?: number; attempt: number; reason: string; status: string; created_at: string }>
}

export async function getLeaseTimeline(taskID: number): Promise<LeaseTimeline> {
  const response = await api<{ ok: boolean; timeline: LeaseTimeline }>(`/api/admin/tasks/${taskID}/leases`)
  return response.timeline
}

function leaseAdminAction(taskID: number, stepID: number, action: string, body?: unknown) {
  return api<{ ok: boolean; lease?: TaskLease }>(`/api/admin/tasks/${taskID}/steps/${stepID}/lease/${action}`, {
    method: 'POST', body: body === undefined ? undefined : JSON.stringify(body)
  })
}

export function extendLeaseDeadline(taskID: number, stepID: number, leaseID: string, leaseVersion: number, resumeDeadline: string) {
  return leaseAdminAction(taskID, stepID, 'extend', { lease_id: leaseID, lease_version: leaseVersion, resume_deadline: resumeDeadline })
}
export function freezeLease(taskID: number, stepID: number, reason: string) { return leaseAdminAction(taskID, stepID, 'freeze', { reason }) }
export function unfreezeLease(taskID: number, stepID: number) { return leaseAdminAction(taskID, stepID, 'unfreeze') }
export function reassignLease(taskID: number, stepID: number, newAgent: string, options: { checkpoint_id?: number; immediate?: boolean; reason?: string } = {}) {
  return leaseAdminAction(taskID, stepID, 'reassign', { new_agent: newAgent, ...options })
}

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
	step_id: number
	report_id: number
	report_version: number
	source_commit: string
	project_lead: string
}

export interface TestEvidence {
  command: string
  status: string
  summary?: string
}

export interface CompletionReport {
  id: number
  agent_name: string
  agent_role: string
  version: number
  source_branch: string
  checkpoint_commit: string
  head_commit: string
  completed: string[]
  incomplete: string[]
  key_files: string[]
  tests: TestEvidence[]
  risks: string[]
  user_decision?: string
  created_at: string
}

export interface MRReview {
  id: number
  reviewer: string
  role: string
  status: string
  body: string
  created_at: string
}

export interface MergeRequestDetail {
  merge_request: MergeRequest
  report: CompletionReport
  reviews: MRReview[]
}

export async function listMergeRequests(): Promise<MergeRequestDetail[]> {
  const response = await api<{ ok: boolean; merge_requests: MergeRequestDetail[] }>('/api/admin/mrs?limit=100')
  return response.merge_requests
}

export async function getMergeRequest(id: number): Promise<MergeRequestDetail> {
  const response = await api<{ ok: boolean; detail: MergeRequestDetail }>(`/api/admin/mrs/${id}`)
  return response.detail
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

export interface DeliveryEvidence { merge_requests: Array<{id:number;step_id:number;status:string;source_commit:string;merge_commit:string;agent_name:string}>; reports:Array<{id:number;step_id:number;agent_name:string;completed:string[];key_files:string[]}>; tests:TestEvidence[]; risks:string[]; incomplete:string[] }
export interface DeliverySnapshot { id:number;task_id:number;project_id:number;version:number;manager_notification_id:number;main_commit:string;status:string;summary:string;summary_hash:string;evidence:DeliveryEvidence;created_by:string;created_at:string }
export interface AcceptanceDecision { id:number;snapshot_id:number;task_id:number;decision:string;comment:string;created_by:string;created_at:string }
export interface ReworkRound { id:number;task_id:number;source_snapshot_id:number;decision_id:number;round:number;plan_version:number;reason:string;status:string;last_error?:string;created_by:string;created_at:string }
export interface DeliveryDetail { snapshot:DeliverySnapshot;decisions:AcceptanceDecision[];rework_rounds:ReworkRound[] }
export async function listDeliveries():Promise<DeliverySnapshot[]>{const response=await api<{ok:boolean;deliveries:DeliverySnapshot[]}>('/api/admin/deliveries');return response.deliveries}
export async function getDelivery(id:number):Promise<DeliveryDetail>{const response=await api<{ok:boolean;detail:DeliveryDetail}>(`/api/admin/deliveries/${id}`);return response.detail}
export async function decideDelivery(id:number,input:{decision:'accepted'|'rejected'|'revision_requested';comment:string;idempotency_key:string}){const response=await api<{ok:boolean;result:{decision:AcceptanceDecision;rework_round?:ReworkRound;task_status:string}}>(`/api/admin/deliveries/${id}/decisions`,{method:'POST',body:JSON.stringify(input)});return response.result}
