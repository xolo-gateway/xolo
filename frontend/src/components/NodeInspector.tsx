import { useEffect, useState } from 'react'
import type { Node } from '@xyflow/react'
import { useReactFlow } from '@xyflow/react'
import type { ModelNodeData, ModelRefNodeData, NodeTypeDescriptor, PipelineNodeType, PluginNodeData, ValueNodeData } from '../types'
import { SCHEMA_CONFIGURED_KINDS } from '../types'
import { builtinTitle, LABELLED_KINDS } from '../nodes/builtin'
import { ModelPicker } from './ModelPicker'
import { SchemaForm } from './SchemaForm'
import { KIND_LABEL, NodeKindIcon } from '../nodes/kind'
import { builtinPorts, pluginPorts, type ResolvedPort } from '../nodes/ports'
import { PORT_COLOR } from '../nodes/PortRow'
import { orgSlug } from '../api'
import { useInspectorWidth } from './useInspectorWidth'

interface NodeInspectorProps {
  node: Node | null
  descriptors: NodeTypeDescriptor[]
  readonly: boolean
  baseUrl: string
}

/**
 * NodeInspector is the right-hand panel of the mockup: everything about the
 * selected node, in one fixed place.
 *
 * It replaces the per-node "Configurer" button and its modal. A modal hides the
 * graph while you configure a node, which is exactly when you need to see what
 * it is connected to; a panel does not. Plugin-provided UIs are embedded here
 * too, in an iframe, rather than keeping a second, modal path alive.
 */
export function NodeInspector({ node, descriptors, readonly, baseUrl }: NodeInspectorProps) {
  const kind = node ? (node.type as PipelineNodeType) : null
  const descriptor = node ? descriptorFor(node, descriptors) : undefined

  // A plugin that brings its own configuration document needs the wide panel;
  // everything else fits the width of the mockup. Deriving the mode from the
  // selection is what makes the panel open at the right size instead of asking
  // the user to resize it every time they click a plugin.
  const hasFrame = kind === 'plugin' && descriptor?.hasUI === true

  const { width, onPointerDown } = useInspectorWidth(hasFrame ? 'wide' : 'compact')

  // With nothing selected the panel has nothing to say, and the canvas is what
  // the user is working in — so it gives the room back rather than standing
  // there holding a sentence.
  if (!node || !kind) return null

  return (
    <aside className="pipeline-inspector" style={{ width }}>
      <div
        className="pipeline-inspector__resizer"
        onPointerDown={onPointerDown}
        title="Redimensionner le panneau"
      />

      <header className="pipeline-inspector__header">
        <span className={`pipeline-inspector__kind pipeline-inspector__kind--${kind}`}>
          <NodeKindIcon kind={kind} />
          {KIND_LABEL[kind]}
        </span>
        <span className="pipeline-inspector__name">{inspectorTitle(node, kind)}</span>
      </header>

      <div className={hasFrame ? 'pipeline-inspector__body pipeline-inspector__body--frame' : 'pipeline-inspector__body'}>
        <Field label="Identifiant du nœud">
          {/* The id is what edges reference; it is shown so a graph can be read
              against its exported JSON, and read-only because renaming it would
              orphan every edge pointing at it. */}
          <input className="pipeline-inspector__input" value={node.id} readOnly />
        </Field>

        {LABELLED_KINDS.has(kind) && <LabelField node={node} readonly={readonly} />}

        <NodeConfig node={node} kind={kind} descriptor={descriptor} readonly={readonly} baseUrl={baseUrl} />

        <PortList node={node} kind={kind} descriptor={descriptor} />

        {!readonly && node.deletable !== false && <DeleteNodeButton nodeId={node.id} />}
      </div>
    </aside>
  )
}

// ─── Free label ─────────────────────────────────────────────────────────────

/**
 * LabelField lets the author name a node in their own words. The card shows
 * the label instead of its computed summary, so a graph can say what each
 * step decides rather than how it is configured.
 */
function LabelField({ node, readonly }: { node: Node; readonly: boolean }) {
  const { updateNodeData } = useReactFlow()
  const label = (node.data as { label?: string }).label ?? ''
  return (
    <Field label="Libellé" hint="optionnel">
      <input
        className="pipeline-inspector__input"
        value={label}
        placeholder="ex. modèle selon complexité"
        disabled={readonly}
        onChange={e => updateNodeData(node.id, { label: e.target.value })}
      />
    </Field>
  )
}

// ─── Per-kind configuration ─────────────────────────────────────────────────

interface NodeConfigProps {
  node: Node
  kind: PipelineNodeType
  descriptor: NodeTypeDescriptor | undefined
  readonly: boolean
  baseUrl: string
}

function NodeConfig({ node, kind, descriptor, readonly, baseUrl }: NodeConfigProps) {
  const { updateNodeData } = useReactFlow()

  if (kind === 'model') {
    const data = node.data as ModelNodeData
    return (
      <>
        <Field label="Modèle appelé" hint="proxyName">
          <ModelPicker
            value={data.proxyName ?? ''}
            disabled={readonly || data.passthrough === true}
            onChange={proxyName => updateNodeData(node.id, { proxyName })}
          />
          <p className="pipeline-inspector__hint">
            Ignoré si le port <code>model_name</code> est connecté : la valeur d'exécution prend le dessus.
          </p>
        </Field>
        <Field>
          <label className="pipeline-inspector__switch">
            <input
              type="checkbox"
              checked={data.passthrough === true}
              disabled={readonly}
              onChange={e => updateNodeData(node.id, { passthrough: e.target.checked })}
            />
            <span>
              <span className="pipeline-inspector__switch-label">Passthrough</span>
              <span className="pipeline-inspector__hint">
                Appelle le modèle demandé par l'appelant au lieu d'un modèle fixe.
                Réservé aux pipelines de middleware.
              </span>
            </span>
          </label>
        </Field>
      </>
    )
  }

  if (kind === 'model_ref') {
    const data = node.data as ModelRefNodeData
    return (
      <Field label="Modèle" hint="proxyName">
        <ModelPicker
          value={data.proxyName ?? ''}
          disabled={readonly}
          onChange={proxyName => updateNodeData(node.id, { proxyName })}
        />
        <p className="pipeline-inspector__hint">
          Le nom choisi est émis sur le port <code>model_name</code>, à brancher sur un nœud modèle, un routeur ou un classifieur.
        </p>
      </Field>
    )
  }

  if (kind === 'model_fallback') {
    const models = ((node.data as { models?: string[] }).models ?? [])
    const setModels = (next: string[]) => updateNodeData(node.id, { models: next })
    return (
      <Field label="Modèles, par ordre de préférence">
        <div className="pipeline-fallback-list">
          {models.map((m, idx) => (
            <div key={idx} className="pipeline-fallback-list__row">
              <span className="pipeline-fallback-list__rank">{idx + 1}.</span>
              <ModelPicker
                value={m}
                disabled={readonly}
                onChange={proxyName => setModels(models.map((x, i) => (i === idx ? proxyName : x)))}
              />
              {!readonly && (
                <button
                  type="button"
                  className="schema-form__row-remove"
                  title="Retirer"
                  onClick={() => setModels(models.filter((_, i) => i !== idx))}
                >
                  ×
                </button>
              )}
            </div>
          ))}
          {!readonly && (
            <button type="button" className="schema-form__add" onClick={() => setModels([...models, ''])}>
              + Ajouter un modèle
            </button>
          )}
        </div>
        <p className="pipeline-inspector__hint">
          Le premier modèle est appelé ; en cas d'erreur, de délai ou de 429, le suivant prend le relais.
          Un port <code>model_name</code> connecté passe avant la liste. Modèles réels uniquement.
        </p>
      </Field>
    )
  }

  if (kind === 'note') {
    const text = (node.data as { text?: string }).text ?? ''
    return (
      <Field label="Texte">
        <textarea
          className="pipeline-inspector__input schema-form__textarea"
          value={text}
          disabled={readonly}
          onChange={e => updateNodeData(node.id, { text: e.target.value })}
        />
      </Field>
    )
  }

  if (SCHEMA_CONFIGURED_KINDS.has(kind)) {
    if (!descriptor?.configSchema) {
      return <p className="pipeline-inspector__hint">Ce nœud n'a rien à configurer : tout passe par ses ports.</p>
    }
    // Built-in nodes keep their configuration at the root of `data`; the
    // descriptor is carried alongside and must survive the replace.
    const { __descriptor, ...config } = node.data as Record<string, unknown>
    return (
      <Field label="Configuration">
        <SchemaForm
          schemaJSON={descriptor.configSchema}
          value={config}
          disabled={readonly}
          onChange={next => updateNodeData(node.id, { ...next, __descriptor }, { replace: true })}
        />
      </Field>
    )
  }

  if (kind === 'value') {
    const data = node.data as ValueNodeData
    const portType = data.portType ?? 'string'
    return (
      <>
        <Field label="Type">
          <select
            className="pipeline-inspector__input"
            value={portType}
            disabled={readonly}
            onChange={e => updateNodeData(node.id, { portType: e.target.value })}
          >
            <option value="string">string</option>
            <option value="number">number</option>
            <option value="boolean">boolean</option>
          </select>
        </Field>
        <Field label="Valeur">
          {portType === 'boolean' ? (
            <select
              className="pipeline-inspector__input"
              value={data.value ?? 'true'}
              disabled={readonly}
              onChange={e => updateNodeData(node.id, { value: e.target.value })}
            >
              <option value="true">true</option>
              <option value="false">false</option>
            </select>
          ) : (
            <input
              className="pipeline-inspector__input"
              type={portType === 'number' ? 'number' : 'text'}
              value={data.value ?? ''}
              placeholder={portType === 'number' ? '0.7' : 'org/claude'}
              disabled={readonly}
              onChange={e => updateNodeData(node.id, { value: e.target.value })}
            />
          )}
        </Field>
      </>
    )
  }

  if (kind === 'plugin' && descriptor?.hasUI) {
    return <PluginConfigFrame node={node} baseUrl={baseUrl} />
  }

  if (kind === 'plugin' && descriptor?.configSchema) {
    // No UI of its own, but a schema: the form is generated from it. It writes
    // straight into the node's config, which is what the graph serialises.
    const data = node.data as PluginNodeData
    return (
      <Field label="Configuration">
        <SchemaForm
          schemaJSON={descriptor.configSchema}
          value={(data.config ?? {}) as Record<string, unknown>}
          disabled={readonly}
          onChange={config => updateNodeData(node.id, { config })}
        />
      </Field>
    )
  }

  if (kind === 'plugin') {
    return (
      <p className="pipeline-inspector__hint">
        Ce plugin n'expose pas d'interface de configuration.
      </p>
    )
  }

  return null
}

// ─── Plugin UI, embedded ────────────────────────────────────────────────────

interface PluginConfigFrameProps {
  node: Node
  baseUrl: string
}

function isPersonalContext(): boolean {
  const root = document.getElementById('pipeline-editor-root')
  return root?.dataset.contextType === 'personal'
}

function configURL(pluginName: string, base: string): string {
  const plugin = encodeURIComponent(pluginName)
  return isPersonalContext()
    ? `${base}/api/personal-plugin-ui-config?plugin=${plugin}`
    : `${base}/api/orgs/${orgSlug()}/plugin-ui-config?plugin=${plugin}`
}

/**
 * PluginConfigFrame embeds a plugin's own configuration UI.
 *
 * The exchange protocol is the one the modal already used and that plugin UIs
 * are written against: the node's config is seeded server-side before the frame
 * loads, the plugin UI reads and writes it there, and the result is read back
 * into the graph. Only the container changed, not the contract — rewriting it
 * would break every existing plugin.
 *
 * The read-back happens when the selection moves away, since a panel has no
 * "close"; the editor also flushes it before saving (see flushPluginConfig).
 */
function PluginConfigFrame({ node, baseUrl }: PluginConfigFrameProps) {
  const data = node.data as PluginNodeData
  const { updateNodeData } = useReactFlow()
  const [ready, setReady] = useState(false)

  useEffect(() => {
    let cancelled = false
    setReady(false)

    fetch(configURL(data.pluginName, baseUrl), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ configJson: JSON.stringify(data.config ?? {}) }),
    })
      .catch(() => undefined) // Open the UI anyway: it can still seed itself.
      .then(() => {
        if (!cancelled) setReady(true)
      })

    return () => {
      cancelled = true
      // Read the config back on deselection. The node is still in the graph, so
      // applying the result once the request lands is safe.
      readPluginConfig(data.pluginName, baseUrl).then(cfg => {
        if (cfg) updateNodeData(node.id, { config: cfg })
      })
    }
  }, [node.id, data.pluginName, baseUrl])

  const uiURL =
    (isPersonalContext()
      ? `${baseUrl}/profile/plugins/${encodeURIComponent(data.pluginName)}/ui/`
      : `${baseUrl}/orgs/${orgSlug()}/plugins/${encodeURIComponent(data.pluginName)}/ui/`) +
    `?nodeId=${encodeURIComponent(node.id)}`

  return (
    <div className="pipeline-inspector__plugin-ui">
      <div className="pipeline-inspector__label">Configuration</div>
      {ready ? (
        <iframe src={uiURL} title={`Configuration ${data.pluginName}`} className="pipeline-inspector__iframe" />
      ) : (
        <div className="pipeline-inspector__hint">Chargement…</div>
      )}
    </div>
  )
}

/** readPluginConfig returns the config the plugin UI last wrote, or null. */
export async function readPluginConfig(
  pluginName: string,
  baseUrl: string
): Promise<Record<string, unknown> | null> {
  try {
    const res = await fetch(configURL(pluginName, baseUrl))
    if (!res.ok) return null
    const body = (await res.json()) as { configJson: string }
    return JSON.parse(body.configJson) as Record<string, unknown>
  } catch {
    return null
  }
}

// ─── Ports ──────────────────────────────────────────────────────────────────

function PortList({
  node,
  kind,
  descriptor,
}: {
  node: Node
  kind: PipelineNodeType
  descriptor: NodeTypeDescriptor | undefined
}) {
  const { inputs, outputs } = inspectorPorts(node, kind, descriptor)

  if (inputs.length === 0 && outputs.length === 0) return null

  return (
    <div className="pipeline-inspector__ports">
      <div className="pipeline-inspector__label">Ports</div>
      {inputs.map(p => (
        <PortEntry key={`in-${p.name}`} port={p} direction={p.required ? 'requis' : 'entrée'} />
      ))}
      {outputs.map(p => (
        <PortEntry key={`out-${p.name}`} port={p} direction="sortie" />
      ))}
    </div>
  )
}

function PortEntry({ port, direction }: { port: ResolvedPort; direction: string }) {
  return (
    <div className="pipeline-inspector__port">
      <span
        className="pipeline-inspector__port-dot"
        style={{ background: PORT_COLOR[port.port_type] ?? 'var(--muted-foreground)' }}
      />
      <span className="pipeline-inspector__port-name">{port.name}</span>
      <span className="pipeline-inspector__port-type">{port.port_type}</span>
      <span className={`pipeline-inspector__port-badge pipeline-inspector__port-badge--${direction}`}>
        {direction}
      </span>
    </div>
  )
}

function DeleteNodeButton({ nodeId }: { nodeId: string }) {
  const { deleteElements } = useReactFlow()
  return (
    <button
      type="button"
      className="pipeline-inspector__delete"
      onClick={() => deleteElements({ nodes: [{ id: nodeId }] })}
    >
      Supprimer le nœud
    </button>
  )
}

// ─── Small pieces ───────────────────────────────────────────────────────────

function Field({ label, hint, children }: { label?: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="pipeline-inspector__field">
      {label && (
        <div className="pipeline-inspector__label">
          {label}
          {hint && <span className="pipeline-inspector__label-hint">{hint}</span>}
        </div>
      )}
      {children}
    </div>
  )
}

// ─── Shared resolution ──────────────────────────────────────────────────────

function descriptorFor(node: Node, descriptors: NodeTypeDescriptor[]): NodeTypeDescriptor | undefined {
  if (node.type === 'plugin') {
    const name = (node.data as PluginNodeData).pluginName
    return descriptors.find(d => d.pluginName === name)
  }
  return descriptors.find(d => d.type === node.type)
}

/**
 * inspectorPorts lists a node's ports. Plugin ports come from the shared
 * resolution so the panel and the card cannot disagree; the built-in kinds
 * declare theirs in the catalog, which the descriptor carries.
 */
export function inspectorPorts(
  node: Node,
  kind: PipelineNodeType,
  descriptor: NodeTypeDescriptor | undefined
): { inputs: ResolvedPort[]; outputs: ResolvedPort[] } {
  if (kind === 'plugin') {
    return pluginPorts(node.data as PluginNodeData, descriptor)
  }
  return builtinPorts(node.data as Record<string, unknown>, descriptor)
}

function labelOr(node: Node, fallback: string): string {
  const label = (node.data as { label?: string }).label?.trim()
  return label || fallback
}

function inspectorTitle(node: Node, kind: PipelineNodeType): string {
  switch (kind) {
    case 'plugin':
      return (node.data as PluginNodeData).pluginName
    case 'model': {
      const data = node.data as ModelNodeData
      return labelOr(node, data.passthrough ? 'modèle demandé' : data.proxyName || 'non configuré')
    }
    case 'model_ref':
      return labelOr(node, (node.data as ModelRefNodeData).proxyName || 'non configuré')
    case 'value':
      return labelOr(node, (node.data as ValueNodeData).value || '—')
    case 'generator':
      return 'chat.completions'
    case 'sink':
      return 'réponse'
    default:
      return builtinTitle(kind, node.data as Record<string, unknown>)
  }
}
