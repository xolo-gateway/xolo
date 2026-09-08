import type { Node } from '@xyflow/react'
import type { NodeTypeDescriptor, PipelineNodeType } from '../types'

/**
 * builtinTitle names a built-in logic node: the author's label when there is
 * one, the node type otherwise. The type is what a reader recognises first on
 * a canvas; how the node is configured is the subtitle's job (see
 * builtinSummary).
 */
export function builtinTitle(kind: PipelineNodeType, data: Record<string, unknown>): string {
  const label = typeof data.label === 'string' ? data.label.trim() : ''
  return label || kind
}

/**
 * builtinSummary condenses a node's configuration into a few characters, the
 * way a reader would say it: "> 0.6" for a comparison, "avg" for a math node,
 * "10 % par utilisateur" for a sample. Empty when there is nothing to say.
 */
export function builtinSummary(kind: PipelineNodeType, data: Record<string, unknown>): string {
  switch (kind) {
    case 'compare': {
      const op = (data.op as string) ?? 'gt'
      const sym: Record<string, string> = { gt: '>', gte: '≥', lt: '<', lte: '≤', eq: '=', ne: '≠' }
      return `${sym[op] ?? op} ${data.threshold ?? 0.5}`
    }
    case 'select':
      return 'condition ? when_true : when_false'
    case 'math':
      return (data.op as string) || 'sum'
    case 'sample': {
      const key = (data.key as string) ?? 'user'
      const by: Record<string, string> = { user: 'par utilisateur', token: 'par jeton', random: 'aléatoire' }
      return `${data.percent ?? 10} % ${by[key] ?? key}`
    }
    case 'context':
      return (data.timezone as string) || 'UTC'
    case 'trace': {
      const inputs = (data.inputs as Array<{ name?: string }> | undefined) ?? []
      return `${inputs.length} port${inputs.length > 1 ? 's' : ''}`
    }
    case 'note': {
      const text = ((data.text as string) ?? '').trim()
      const first = text.split('\n')[0]
      return first ? (first.length > 32 ? first.slice(0, 32) + '…' : first) : ''
    }
    case 'model_fallback': {
      const models = (data.models as string[] | undefined)?.filter(Boolean) ?? []
      if (models.length === 0) return 'non configuré'
      return models.length > 1 ? `${models[0]} +${models.length - 1}` : models[0]
    }
    default:
      return ''
  }
}

/** LABELLED_KINDS are the built-in kinds whose title can be set by hand. */
export const LABELLED_KINDS: ReadonlySet<PipelineNodeType> = new Set<PipelineNodeType>([
  'model',
  'model_ref',
  'model_fallback',
  'value',
  'compare',
  'select',
  'math',
  'sample',
  'context',
  'trace',
])

/** descriptorOf reads the catalog descriptor the editor attached to a node. */
export function descriptorOf(node: Node): NodeTypeDescriptor | undefined {
  return (node.data as { __descriptor?: NodeTypeDescriptor }).__descriptor
}
