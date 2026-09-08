import type { NodeProps } from '@xyflow/react'
import { NodeCard } from './NodeCard'
import { builtinSummary, builtinTitle } from './builtin'
import { builtinPorts } from './ports'
import type { NodeTypeDescriptor, PipelineNodeType } from '../types'

/**
 * BuiltinNode draws any built-in logic node from its catalog descriptor: the
 * ports come from the server, the title from the node's data. One component
 * for eight kinds keeps the canvas consistent and means adding a built-in node
 * is a server-side change plus a label, not a new React component.
 */
export function BuiltinNode({ type, data }: NodeProps) {
  const kind = type as PipelineNodeType
  const nodeData = data as Record<string, unknown> & { __descriptor?: NodeTypeDescriptor }
  const desc = nodeData.__descriptor
  const { inputs, outputs } = builtinPorts(nodeData, desc)

  return (
    <NodeCard
      kind={kind}
      title={builtinTitle(kind, nodeData)}
      subtitle={builtinSummary(kind, nodeData) || desc?.label}
      inputs={inputs.map(p => ({ name: p.name, port_type: p.port_type, label: p.name }))}
      outputs={outputs.map(p => ({ name: p.name, port_type: p.port_type, label: p.name }))}
    />
  )
}
