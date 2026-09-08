import type { VirtualModel, NodeTypeDescriptor, PipelineGraph, PipelineBundle, PipelineModelOption } from './types'

function getBase(): string {
  const root = document.getElementById('pipeline-editor-root')
  return root?.dataset.apiBaseUrl ?? ''
}

function orgSlug(): string {
  const root = document.getElementById('pipeline-editor-root')
  return root?.dataset.orgSlug ?? ''
}

function vmId(): string | null {
  const root = document.getElementById('pipeline-editor-root')
  return root?.dataset.vmId ?? null
}

function contextType(): string {
  const root = document.getElementById('pipeline-editor-root')
  return root?.dataset.contextType ?? ''
}

function isPersonalContext(): boolean {
  return contextType() === 'personal'
}

function isMiddlewareContext(): boolean {
  return contextType() === 'middleware'
}

// entityBase returns the REST base path of the pipeline-bearing entity being
// edited (virtual model, personal virtual model or middleware).
function entityBase(): string {
  if (isPersonalContext()) return `/api/personal-models`
  if (isMiddlewareContext()) return `/api/orgs/${orgSlug()}/middlewares`
  return `/api/orgs/${orgSlug()}/virtual-models`
}

// nodeTypesBase returns the base path exposing the pipeline node-type catalog.
function nodeTypesBase(): string {
  if (isPersonalContext()) return `/api/personal-models/pipeline-node-types`
  return `/api/orgs/${orgSlug()}/pipeline-node-types`
}

export function isReadonly(): boolean {
  const root = document.getElementById('pipeline-editor-root')
  return root?.dataset.readonly === 'true'
}

/**
 * backLink is the way out of the editor: the list the edited entity belongs to.
 *
 * The same editor serves three kinds of pipeline-bearing entity, each reached
 * through its own route, so the target cannot be a fixed relative path — from a
 * middleware, "../../virtual-models" would land on a list that does not contain
 * what you were editing.
 */
export function backLink(): { href: string; label: string } {
  const base = getBase()

  if (isPersonalContext()) {
    return { href: `${base}/profile/personal-models`, label: 'Mes modèles' }
  }
  if (isMiddlewareContext()) {
    return { href: `${base}/orgs/${orgSlug()}/admin/middlewares`, label: 'Middlewares' }
  }
  return { href: `${base}/orgs/${orgSlug()}/admin/virtual-models`, label: 'Modèles virtuels' }
}

export { vmId, orgSlug }

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(getBase() + path, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`API error ${res.status}: ${text}`)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export function fetchVirtualModel(id: string): Promise<VirtualModel> {
  return request(`${entityBase()}/${id}`)
}

export function updateVirtualModel(
  id: string,
  patch: { description?: string; graph?: PipelineGraph }
): Promise<VirtualModel> {
  return request(`${entityBase()}/${id}`, {
    method: 'PUT',
    body: JSON.stringify(patch),
  })
}

export function fetchNodeTypes(): Promise<NodeTypeDescriptor[]> {
  return request(nodeTypesBase())
}

// pipelineModelsBase returns the base path listing what a model_name port can
// name in the current context.
function pipelineModelsBase(): string {
  if (isPersonalContext()) return `/api/personal-models/pipeline-models`
  return `/api/orgs/${orgSlug()}/pipeline-models`
}

let pipelineModelsCache: Promise<PipelineModelOption[]> | null = null

/**
 * fetchPipelineModels lists the models a picker can offer. The list is fetched
 * once per editor session: every model node and model reference shares it, and
 * the catalog does not change while a pipeline is being wired.
 */
export function fetchPipelineModels(): Promise<PipelineModelOption[]> {
  if (!pipelineModelsCache) {
    pipelineModelsCache = request<PipelineModelOption[]>(pipelineModelsBase()).catch(err => {
      pipelineModelsCache = null
      throw err
    })
  }
  return pipelineModelsCache
}

export function exportVirtualModelURL(id: string): string {
  return `${getBase()}${entityBase()}/${id}/export`
}

export function importVirtualModel(bundle: PipelineBundle): Promise<VirtualModel> {
  return request(`${entityBase()}/import`, {
    method: 'POST',
    body: JSON.stringify(bundle),
  })
}
