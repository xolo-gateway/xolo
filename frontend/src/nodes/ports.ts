import type { NodeTypeDescriptor, PluginNodeData, PortDescriptor } from '../types'

export interface ResolvedPort {
  name: string
  port_type: string
  required?: boolean
}

/**
 * pluginPorts resolves the ports a plugin node actually exposes.
 *
 * A plugin declares a default set in its descriptor, but some plugins let the
 * user define their own inputs and outputs through their configuration UI —
 * a router, for instance, has as many branches as the user configured. When the
 * config declares ports they win, because the descriptor then describes a shape
 * the configured node no longer has.
 *
 * Both the card on the canvas and the inspector need this answer, and they must
 * not disagree: a port drawn on the card and absent from the inspector would
 * make the panel look broken.
 */
export function pluginPorts(
  data: PluginNodeData,
  desc: NodeTypeDescriptor | undefined
): { inputs: ResolvedPort[]; outputs: ResolvedPort[] } {
  const cfg = data.config as
    | { inputs?: Array<{ name: string; portType?: string }>; outputs?: Array<{ name: string; portType?: string }> }
    | undefined

  return {
    inputs: cfg?.inputs
      ? cfg.inputs.map(i => ({ name: i.name, port_type: i.portType ?? 'number' }))
      : fromDescriptors(desc?.inputPorts),
    outputs: cfg?.outputs
      ? cfg.outputs.map(o => ({ name: o.name, port_type: o.portType ?? 'number' }))
      : fromDescriptors(desc?.outputPorts),
  }
}

function fromDescriptors(ports: PortDescriptor[] | undefined): ResolvedPort[] {
  return (ports ?? []).map(p => ({ name: p.name, port_type: p.port_type, required: p.required }))
}

/**
 * builtinPorts resolves the ports of a built-in node. Most built-in kinds have
 * a fixed shape declared in the catalog; a few (trace) let the user declare
 * their input ports in the node data, the way script-processor does through
 * its config. When the data declares ports they win.
 */
export function builtinPorts(
  data: Record<string, unknown>,
  desc: NodeTypeDescriptor | undefined
): { inputs: ResolvedPort[]; outputs: ResolvedPort[] } {
  const declared = data.inputs as Array<{ name?: string; portType?: string }> | undefined
  const inputs = Array.isArray(declared)
    ? declared
        .filter(i => i && typeof i.name === 'string' && i.name !== '')
        .map(i => ({ name: i.name as string, port_type: i.portType ?? 'string' }))
    : fromDescriptors(desc?.inputPorts)
  return { inputs, outputs: fromDescriptors(desc?.outputPorts) }
}
