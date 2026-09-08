import type { NodeProps } from '@xyflow/react'
import { NodeCard } from './NodeCard'
import type { ValueNodeData } from '../types'

const TYPE_LABELS: Record<string, string> = {
  string: 'abc',
  number: '123',
  boolean: 'T/F',
}

/**
 * ValueNode shows the value it emits; editing it happens in the inspector.
 *
 * The node used to carry its own inline editor. Moving it out is what makes the
 * canvas readable at a glance: a card that is also a form has no fixed height,
 * and the graph stops being scannable.
 */
export function ValueNode({ data }: NodeProps) {
  const nodeData = data as ValueNodeData
  const portType = nodeData.portType ?? 'string'
  const value = nodeData.value ?? ''

  return (
    <NodeCard
      kind="value"
      title={(typeof nodeData.label === 'string' && nodeData.label.trim()) || (value !== '' ? value : '—')}
      subtitle={portType}
      outputs={[{ name: 'value', port_type: portType, label: TYPE_LABELS[portType] ?? 'value' }]}
    />
  )
}
