import type { Edge, Node } from '@xyflow/react'
import type { NodeTypeDescriptor, PipelineNodeType, PluginNodeData } from './types'
import { inspectorPorts } from './components/NodeInspector'
import { t } from './i18n'

export interface GraphValidation {
  valid: boolean
  /** One sentence stating either that the graph holds, or what is missing. */
  message: string
}

/**
 * validateGraph checks the one invariant the editor can check on its own: every
 * required input port is connected.
 *
 * This is deliberately not a full validation — type compatibility and cycle
 * detection live server-side, where the pipeline is actually compiled. What the
 * editor answers is the question a user asks while wiring: "have I forgotten
 * something?", which is otherwise only discovered on save.
 */
export function validateGraph(
  nodes: Node[],
  edges: Edge[],
  descriptors: NodeTypeDescriptor[]
): GraphValidation {
  const connected = new Set(edges.map(e => portKey(e.target, e.targetHandle ?? '')))
  const missing: string[] = []

  for (const node of nodes) {
    const kind = node.type as PipelineNodeType
    const descriptor = descriptorFor(node, descriptors)

    for (const port of inspectorPorts(node, kind, descriptor).inputs) {
      if (!port.required) continue
      if (connected.has(portKey(node.id, port.name))) continue
      missing.push(`${nodeLabel(node, kind)}.${port.name}`)
    }

    if (kind === 'compare') {
      const problem = compareProblem(node, connected)
      if (problem) return { valid: false, message: problem }
    }
  }

  if (missing.length === 0) {
    return { valid: true, message: t('validate.ok') }
  }

  const head = missing.slice(0, 3).join(', ')
  const rest = missing.length > 3 ? t('validate.missingRest', { count: missing.length - 3 }) : ''

  return {
    valid: false,
    message: t('validate.missing', { ports: head, rest }),
  }
}

/**
 * compareProblem checks the one-of rule the catalog cannot express: a compare
 * needs either its value port or its text port, and the text mode needs an
 * expected string and an equality operator. The engine refuses all three at
 * run time; catching them here keeps the mistake out of the saved graph.
 */
function compareProblem(node: Node, connected: Set<string>): string | null {
  const data = node.data as { op?: string; expected?: string }
  const hasValue = connected.has(portKey(node.id, 'value'))
  const hasText = connected.has(portKey(node.id, 'text'))
  const name = `compare (${node.id})`
  if (!hasValue && !hasText) return t('validate.compare.noInput', { name })
  if (hasValue && hasText) return t('validate.compare.both', { name })
  if (hasText) {
    if (!(data.expected ?? '').trim()) return t('validate.compare.noExpected', { name })
    // The engine reads an absent op as eq on text; only an explicit ordering op is wrong.
    const op = data.op || 'eq'
    if (op !== 'eq' && op !== 'ne') return t('validate.compare.badOperator', { name, op })
  }
  return null
}

/** portKey names one input port of one node; a NUL keeps ids with spaces unambiguous. */
function portKey(nodeId: string, port: string): string {
  return `${nodeId}\u0000${port}`
}

function descriptorFor(node: Node, descriptors: NodeTypeDescriptor[]): NodeTypeDescriptor | undefined {
  if (node.type === 'plugin') {
    const name = (node.data as PluginNodeData).pluginName
    return descriptors.find(d => d.pluginName === name)
  }
  return descriptors.find(d => d.type === node.type)
}

function nodeLabel(node: Node, kind: PipelineNodeType): string {
  if (kind === 'plugin') return (node.data as PluginNodeData).pluginName
  return kind
}
