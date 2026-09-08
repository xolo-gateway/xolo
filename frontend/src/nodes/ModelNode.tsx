import type { NodeProps } from '@xyflow/react'
import type { ModelNodeData } from '../types'
import { NodeCard } from './NodeCard'

// In a middleware pipeline every model node is a passthrough: it wraps the model
// actually requested by the caller (enforced server-side). In other contexts
// (virtual models) the node resolves a model via its model_name input.
function isMiddlewareContext(): boolean {
  const root = document.getElementById('pipeline-editor-root')
  return root?.dataset.contextType === 'middleware'
}

export function ModelNode({ data }: NodeProps) {
  const middleware = isMiddlewareContext()
  const nodeData = data as ModelNodeData
  const passthrough = middleware || nodeData.passthrough === true

  // A passthrough node has no fixed model: naming one would be a lie, since the
  // model is whatever the caller asked for.
  const label = typeof nodeData.label === 'string' ? nodeData.label.trim() : ''
  const title = label || (passthrough ? 'modèle demandé' : (nodeData.proxyName || 'non configuré'))
  const subtitle = passthrough ? 'passthrough' : 'proxyName'

  const inputs = [{ name: 'request', port_type: 'request', label: 'request' }]
  if (!passthrough) {
    inputs.push({ name: 'model_name', port_type: 'string', label: 'model_name' })
  }

  return (
    <NodeCard
      kind="model"
      title={title}
      subtitle={subtitle}
      inputs={inputs}
      outputs={[{ name: 'response', port_type: 'response', label: 'response' }]}
    />
  )
}
