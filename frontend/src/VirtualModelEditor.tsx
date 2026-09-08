import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  addEdge,
  useNodesState,
  useEdgesState,
  type Node,
  type Edge,
  type Connection,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import { GeneratorNode } from './nodes/GeneratorNode'
import { SinkNode } from './nodes/SinkNode'
import { ModelNode } from './nodes/ModelNode'
import { ModelRefNode } from './nodes/ModelRefNode'
import { BuiltinNode } from './nodes/BuiltinNode'
import { NoteNode } from './nodes/NoteNode'
import { ValueNode } from './nodes/ValueNode'
import { PluginNode } from './nodes/PluginNode'
import { NodePalette } from './components/NodePalette'
import { NodeInspector, readPluginConfig } from './components/NodeInspector'
import { PORT_COLOR } from './nodes/PortRow'
import { validateGraph } from './validate'
import { fetchVirtualModel, fetchNodeTypes, updateVirtualModel, vmId, orgSlug, isReadonly, backLink } from './api'
import type { NodeTypeDescriptor, PipelineGraph, PipelineNode, PipelineEdge, PipelineBundle, PluginNodeData } from './types'
import { ReadonlyContext } from './ReadonlyContext'

const nodeTypes = {
  generator: GeneratorNode,
  sink: SinkNode,
  model: ModelNode,
  model_ref: ModelRefNode,
  model_fallback: BuiltinNode,
  compare: BuiltinNode,
  select: BuiltinNode,
  math: BuiltinNode,
  sample: BuiltinNode,
  context: BuiltinNode,
  trace: BuiltinNode,
  note: NoteNode,
  value: ValueNode,
  plugin: PluginNode,
}

// The legend under the canvas names every port type once, so the colours on the
// handles are readable without opening a node.
const PORT_LEGEND: Array<{ type: string; label: string }> = [
  { type: 'request', label: 'request' },
  { type: 'response', label: 'response' },
  { type: 'string', label: 'string' },
  { type: 'number', label: 'number' },
  { type: 'boolean', label: 'boolean' },
]

/** defaultNodeData seeds a new node so its card reads sensibly before any edit. */
function defaultNodeData(desc: NodeTypeDescriptor): Record<string, unknown> {
  switch (desc.type) {
    case 'plugin':
      return { pluginName: desc.pluginName }
    case 'compare':
      return { op: 'gt', threshold: 0.5 }
    case 'math':
      return { op: 'sum' }
    case 'sample':
      return { percent: 10, key: 'user' }
    case 'model_fallback':
      return { models: [] }
    case 'trace':
      return { severity: 'info', inputs: [{ name: 'value', portType: 'number' }] }
    case 'note':
      return { text: '' }
    default:
      return {}
  }
}

let idCounter = 1
function nextId() {
  return `node-${Date.now()}-${idCounter++}`
}

function graphToFlow(graph: PipelineGraph | undefined, descriptors: NodeTypeDescriptor[]): { nodes: Node[]; edges: Edge[] } {
  if (!graph) {
    return {
      nodes: [
        { id: 'gen', type: 'generator', position: { x: 80, y: 200 }, data: {}, deletable: false },
        { id: 'sink', type: 'sink', position: { x: 600, y: 200 }, data: {}, deletable: false },
      ],
      edges: [],
    }
  }

  const descMap = new Map(descriptors.map(d => [d.pluginName ?? d.type, d]))

  // Every node carries its catalog descriptor, so cards can draw their ports
  // without a lookup and built-in kinds need no dedicated component.
  const nodes: Node[] = graph.nodes.map(n => ({
    id: n.id,
    type: n.type,
    position: n.position,
    deletable: n.type !== 'generator' && n.type !== 'sink',
    data: {
      ...(n.data ?? {}),
      __descriptor: descMap.get(n.type === 'plugin' ? (n.data as PluginNodeData)?.pluginName : n.type),
    },
  }))

  const edges: Edge[] = graph.edges.map(e => ({
    id: e.id,
    source: e.source,
    sourceHandle: e.sourcePort,
    target: e.target,
    targetHandle: e.targetPort,
  }))

  return { nodes, edges }
}

function flowToGraph(nodes: Node[], edges: Edge[]): PipelineGraph {
  const pNodes: PipelineNode[] = nodes.map(n => ({
    id: n.id,
    type: n.type as PipelineNode['type'],
    position: n.position,
    data: (({ __descriptor: _d, ...rest }) => rest)(n.data as Record<string, unknown>),
  }))

  const pEdges: PipelineEdge[] = edges.map(e => ({
    id: e.id,
    source: e.source,
    sourcePort: e.sourceHandle ?? '',
    target: e.target,
    targetPort: e.targetHandle ?? '',
  }))

  return { nodes: pNodes, edges: pEdges }
}

export function VirtualModelEditor() {
  const id = vmId()
  const slug = orgSlug()
  const readonly = isReadonly()

  const [vmName, setVmName] = useState('')
  const [vmDescription, setVmDescription] = useState('')
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [descriptors, setDescriptors] = useState<NodeTypeDescriptor[]>([])
  const importInputRef = useRef<HTMLInputElement>(null)

  const [nodes, setNodes, onNodesChange] = useNodesState<Node>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<Edge>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)

  const descriptorsRef = useRef<NodeTypeDescriptor[]>([])
  descriptorsRef.current = descriptors

  const baseUrl = document.getElementById('pipeline-editor-root')?.dataset.apiBaseUrl ?? ''

  useEffect(() => {
    async function load() {
      try {
        const [nts] = await Promise.all([fetchNodeTypes()])
        setDescriptors(nts)

        if (id) {
          const vm = await fetchVirtualModel(id)
          setVmName(vm.name)
          setVmDescription(vm.description ?? '')
          const { nodes: n, edges: e } = graphToFlow(vm.graph, nts)
          setNodes(n)
          setEdges(e)
        }
      } catch (err) {
        setError(String(err))
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [id, setNodes, setEdges])

  const onConnect = useCallback(
    (connection: Connection) => {
      setEdges(eds => addEdge(connection, eds))
    },
    [setEdges]
  )

  function addNode(desc: NodeTypeDescriptor) {
    const newId = nextId()
    const newNode: Node = {
      id: newId,
      type: desc.type,
      position: { x: 300 + Math.random() * 100, y: 200 + Math.random() * 100 },
      deletable: true,
      data: { ...defaultNodeData(desc), __descriptor: desc },
    }
    setNodes(nds => [...nds, newNode])
  }

  /**
   * flushPluginConfigs pulls back whatever the embedded plugin UIs have written
   * server-side, before the graph is serialised.
   *
   * The inspector reads a plugin's config back when the selection moves away,
   * but the node being edited at the moment Save is pressed has never been
   * deselected — without this its last changes would be dropped.
   */
  async function flushPluginConfigs(current: Node[]): Promise<Node[]> {
    const selected = current.find(n => n.id === selectedId && n.type === 'plugin')
    if (!selected) return current

    const cfg = await readPluginConfig((selected.data as PluginNodeData).pluginName, baseUrl)
    if (!cfg) return current

    return current.map(n => (n.id === selected.id ? { ...n, data: { ...n.data, config: cfg } } : n))
  }

  async function save() {
    if (!id) return
    setSaving(true)
    setError(null)
    try {
      const flushed = await flushPluginConfigs(nodes)
      if (flushed !== nodes) setNodes(flushed)
      await updateVirtualModel(id, { graph: flowToGraph(flushed, edges) })
    } catch (err) {
      setError(String(err))
    } finally {
      setSaving(false)
    }
  }

  function exportPipeline() {
    if (!id) return
    const graph = flowToGraph(nodes, edges)
    const bundle: PipelineBundle = {
      version: '1',
      exportedAt: new Date().toISOString(),
      name: vmName,
      description: vmDescription,
      graph,
    }
    const blob = new Blob([JSON.stringify(bundle, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `pipeline-${vmName.replace(/[/\\:*?"<>|]/g, '-')}.json`
    a.click()
    URL.revokeObjectURL(url)
  }

  async function handleImportFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    if (!file) return
    setError(null)
    try {
      const text = await file.text()
      const bundle = JSON.parse(text) as PipelineBundle
      if (!bundle.graph) {
        setError('Le fichier ne contient pas de graphe de pipeline.')
        return
      }
      const { nodes: n, edges: ed } = graphToFlow(bundle.graph, descriptorsRef.current)
      setNodes(n)
      setEdges(ed)
    } catch {
      setError('Impossible de lire le fichier : format invalide.')
    } finally {
      // Reset so the same file can be re-imported if needed.
      e.target.value = ''
    }
  }

  const paletteTypes = useMemo(
    () => descriptors.filter(d => d.type !== 'generator' && d.type !== 'sink'),
    [descriptors]
  )

  const selectedNode = useMemo(
    () => nodes.find(n => n.id === selectedId) ?? null,
    [nodes, selectedId]
  )

  const validation = useMemo(
    () => validateGraph(nodes, edges, descriptors),
    [nodes, edges, descriptors]
  )

  // The qualified name is the one callers use in their requests; showing the
  // bare name would not be the identifier that matters.
  const qualifiedName = slug ? `${slug}/${vmName}` : vmName

  const back = backLink()

  if (loading) return <div className="pipeline-loading">Chargement…</div>

  return (
    <ReadonlyContext.Provider value={readonly}>
      <div className="pipeline-editor">
        <header className="pipeline-editor__toolbar">
          <a className="pipeline-editor__back" href={back.href} title={`Retour — ${back.label}`}>
            ← {back.label}
          </a>

          <span className="pipeline-editor__vm-name">{qualifiedName}</span>

          <span className="pipeline-editor__counts">
            {nodes.length} nœud{nodes.length > 1 ? 's' : ''} · {edges.length} liaison
            {edges.length > 1 ? 's' : ''}
          </span>

          {error && <span className="pipeline-editor__error">{error}</span>}

          <span className="pipeline-editor__actions">
            {!readonly && (
              <>
                <input
                  ref={importInputRef}
                  type="file"
                  accept=".json,application/json"
                  style={{ display: 'none' }}
                  onChange={handleImportFile}
                />
                <button
                  className="pipeline-editor__btn"
                  onClick={() => importInputRef.current?.click()}
                  title="Charger un pipeline depuis un fichier JSON"
                >
                  Importer
                </button>
                {/*
                  TODO(rework-ux): la maquette propose « Aligner » (mise en page
                  automatique du graphe) et « Tester » (exécution à blanc du
                  pipeline contre une requête d'exemple). Ni l'un ni l'autre
                  n'existe : le premier demande un moteur de layout, le second une
                  route d'exécution à blanc côté proxy. Les contrôles sont visibles
                  et inertes (cf. lot 7 du plan).
                */}
                <button className="pipeline-editor__btn" disabled title="Bientôt disponible">
                  Aligner
                </button>
                <button className="pipeline-editor__btn" disabled title="Bientôt disponible">
                  Tester
                </button>
              </>
            )}
            <button
              className="pipeline-editor__btn"
              onClick={exportPipeline}
              disabled={!id}
              title="Télécharger le pipeline courant (incluant la configuration de chaque nœud)"
            >
              Exporter
            </button>
            {!readonly && (
              <button
                className="pipeline-editor__btn pipeline-editor__btn--primary"
                onClick={save}
                disabled={saving || !id}
              >
                {saving ? 'Enregistrement…' : 'Enregistrer'}
              </button>
            )}
          </span>
        </header>

        <div className="pipeline-editor__body">
          {!readonly && <NodePalette nodeTypes={paletteTypes} onAddNode={addNode} />}

          <div className="pipeline-editor__center">
            <div className="pipeline-editor__canvas">
              <ReactFlow
                nodes={nodes}
                edges={edges}
                nodeTypes={nodeTypes}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onConnect={readonly ? undefined : onConnect}
                onSelectionChange={({ nodes: sel }) => setSelectedId(sel[0]?.id ?? null)}
                deleteKeyCode={readonly ? null : ['Backspace', 'Delete']}
                nodesDraggable={!readonly}
                nodesConnectable={!readonly}
                fitView
              >
                <Background />
                <Controls />
                <MiniMap />
              </ReactFlow>
            </div>

            <footer className="pipeline-editor__status">
              <span
                className={
                  validation.valid
                    ? 'pipeline-editor__status-text pipeline-editor__status-text--ok'
                    : 'pipeline-editor__status-text pipeline-editor__status-text--warn'
                }
              >
                <span className="pipeline-editor__status-dot" />
                {validation.message}
              </span>
              <span className="pipeline-editor__legend">
                {PORT_LEGEND.map(p => (
                  <span key={p.type} className="pipeline-editor__legend-item">
                    <span
                      className="pipeline-editor__legend-dot"
                      style={{ background: PORT_COLOR[p.type] }}
                    />
                    {p.label}
                  </span>
                ))}
              </span>
            </footer>
          </div>

          <NodeInspector
            node={selectedNode}
            descriptors={descriptors}
            readonly={readonly}
            baseUrl={baseUrl}
          />
        </div>
      </div>
    </ReadonlyContext.Provider>
  )
}
