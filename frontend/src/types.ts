// Mirrors Go model.PipelineNodeType
export type PipelineNodeType =
  | 'generator'
  | 'sink'
  | 'model'
  | 'model_ref'
  | 'model_fallback'
  | 'compare'
  | 'select'
  | 'math'
  | 'sample'
  | 'context'
  | 'trace'
  | 'note'
  | 'value'
  | 'plugin'

// Port type mirrors model.PortType
export type PortType = 'request' | 'response' | 'number' | 'string' | 'boolean'

export interface PortDescriptor {
  name: string
  port_type: PortType
  required?: boolean
}

export interface NodeTypeDescriptor {
  type: PipelineNodeType
  pluginName?: string
  /** Plugin's own version, shown in the palette. Empty for built-in types. */
  version?: string
  label: string
  description: string
  inputPorts: PortDescriptor[]
  outputPorts: PortDescriptor[]
  configSchema?: string
  hasUI?: boolean
  /** Plugin capabilities, e.g. "POST_RESPONSE". Absent for built-in types. */
  capabilities?: string[]
}

// Pipeline graph structures (mirrors Go model.PipelineGraph)
export interface PipelineGraph {
  nodes: PipelineNode[]
  edges: PipelineEdge[]
}

export interface PipelineNode {
  id: string
  type: PipelineNodeType
  position: { x: number; y: number }
  data?: Record<string, unknown>
}

export interface PipelineEdge {
  id: string
  source: string
  sourcePort: string
  target: string
  targetPort: string
}

export interface VirtualModel {
  id: string
  orgId: string
  name: string
  description: string
  graph?: PipelineGraph
  createdAt: string
  updatedAt: string
}

// Mirrors Go model.PipelineBundle
export interface PipelineBundle {
  version: string
  exportedAt: string
  name: string
  description: string
  graph?: PipelineGraph
}

// Node data payloads.
//
// React Flow types a node's `data` as Record<string, unknown>, so each payload
// carries an index signature: without it every read from a node requires a
// double cast through `unknown`, which defeats the point of typing them.
export interface PluginNodeData {
  pluginName: string
  config?: Record<string, unknown>
  [key: string]: unknown
}

export interface ModelNodeData {
  [key: string]: unknown
  proxyName?: string
  /**
   * Passthrough resolves the model the caller asked for instead of a fixed one.
   * Middleware pipelines are always passthrough; virtual models may opt in.
   */
  passthrough?: boolean
}

export interface ValueNodeData {
  [key: string]: unknown
  portType?: 'string' | 'number' | 'boolean'
  value?: string
}


export interface ModelRefNodeData {
  [key: string]: unknown
  /** Proxy name of the chosen model, emitted on the model_name port. */
  proxyName?: string
}

/** One entry of the model picker (mirrors Go api.pipelineModelOption). */
export interface PipelineModelOption {
  proxyName: string
  kind: 'model' | 'virtual' | 'personal'
  description?: string
  group?: string
}

/** Node kinds whose configuration lives at the root of `data` and is edited
 *  through the form generated from the catalog's configSchema. */
export const SCHEMA_CONFIGURED_KINDS: ReadonlySet<PipelineNodeType> = new Set<PipelineNodeType>([
  'compare',
  'select',
  'math',
  'sample',
  'context',
  'trace',
])
