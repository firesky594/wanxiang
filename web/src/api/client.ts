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
