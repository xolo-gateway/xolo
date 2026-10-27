import { NodeCard } from './NodeCard'
import { t } from '../i18n'

export function SinkNode() {
  return (
    <NodeCard
      kind="sink"
      title={t('node.sink.title')}
      subtitle={t('node.sink.subtitle')}
      inputs={[{ name: 'response', port_type: 'response' }]}
    />
  )
}
