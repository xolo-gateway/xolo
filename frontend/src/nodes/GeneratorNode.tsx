import { NodeCard } from './NodeCard'
import { t } from '../i18n'

export function GeneratorNode() {
  return (
    <NodeCard
      kind="generator"
      title="chat.completions"
      subtitle={t('node.generator.subtitle')}
      outputs={[{ name: 'request', port_type: 'request' }]}
    />
  )
}
