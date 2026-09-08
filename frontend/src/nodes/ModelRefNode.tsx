import type { NodeProps } from '@xyflow/react'
import { NodeCard } from './NodeCard'
import type { ModelRefNodeData } from '../types'

/**
 * ModelRefNode names a model of the catalog and emits that name. It is what a
 * value node would be if it knew which strings are model names: the same
 * single string port, but a picker instead of a text field in the inspector.
 */
export function ModelRefNode({ data }: NodeProps) {
  const nodeData = data as ModelRefNodeData
  return (
    <NodeCard
      kind="model_ref"
      title={(typeof nodeData.label === 'string' && nodeData.label.trim()) || nodeData.proxyName || 'non configuré'}
      subtitle={nodeData.label ? nodeData.proxyName || 'proxyName' : 'proxyName'}
      outputs={[{ name: 'model_name', port_type: 'string', label: 'model_name' }]}
    />
  )
}
