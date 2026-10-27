import type { NodeTypeDescriptor } from '../types'
import { KIND_DESCRIPTION, KIND_LABEL, NodeKindIcon } from '../nodes/kind'
import { t } from '../i18n'

interface NodePaletteProps {
  nodeTypes: NodeTypeDescriptor[]
  onAddNode: (descriptor: NodeTypeDescriptor) => void
}

/**
 * NodePalette lists what can be dropped on the canvas, split the way the mockup
 * splits it: the built-in kinds the runtime always understands, then the
 * plugins, which are whatever the server has loaded.
 *
 * The distinction matters to a reader: a built-in node behaves identically in
 * every deployment, a plugin node depends on a binary that may not exist on the
 * next one — which is also why plugins carry their version here.
 */
export function NodePalette({ nodeTypes, onAddNode }: NodePaletteProps) {
  const builtins = nodeTypes.filter(nd => nd.type !== 'plugin')
  const plugins = nodeTypes.filter(nd => nd.type === 'plugin')

  return (
    <aside className="pipeline-palette">
      {builtins.length > 0 && (
        <section className="pipeline-palette__group">
          <h3 className="pipeline-palette__title">{t('palette.builtins')}</h3>
          <ul className="pipeline-palette__list">
            {builtins.map(nd => (
              <PaletteItem
                key={nd.type}
                descriptor={nd}
                subtitle={KIND_DESCRIPTION[nd.type]}
                onAdd={onAddNode}
              />
            ))}
          </ul>
        </section>
      )}

      {plugins.length > 0 && (
        <section className="pipeline-palette__group">
          <h3 className="pipeline-palette__title">
            {t('palette.plugins')} <span className="pipeline-palette__count">· {plugins.length}</span>
          </h3>
          <ul className="pipeline-palette__list">
            {plugins.map(nd => (
              <PaletteItem
                key={nd.pluginName}
                descriptor={nd}
                subtitle={nd.version ? `v${nd.version}` : ''}
                mono
                onAdd={onAddNode}
              />
            ))}
          </ul>
        </section>
      )}
    </aside>
  )
}

interface PaletteItemProps {
  descriptor: NodeTypeDescriptor
  subtitle: string
  mono?: boolean
  onAdd: (descriptor: NodeTypeDescriptor) => void
}

function PaletteItem({ descriptor, subtitle, mono, onAdd }: PaletteItemProps) {
  return (
    <li>
      <button
        type="button"
        className="pipeline-palette__item"
        onClick={() => onAdd(descriptor)}
        title={descriptor.description}
      >
        <span className={`pipeline-palette__icon pipeline-palette__icon--${descriptor.type}`}>
          <NodeKindIcon kind={descriptor.type} />
        </span>
        <span className="pipeline-palette__text">
          <span className={mono ? 'pipeline-palette__label pipeline-palette__label--mono' : 'pipeline-palette__label'}>
            {descriptor.pluginName ?? KIND_LABEL[descriptor.type]}
          </span>
          {subtitle && <span className="pipeline-palette__subtitle">{subtitle}</span>}
        </span>
      </button>
    </li>
  )
}
