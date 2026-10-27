import type { NodeProps } from '@xyflow/react'
import { NodeCard } from './NodeCard'
import { t } from '../i18n'

/** NoteNode shows its text in full: a note that has to be opened is not read. */
export function NoteNode({ data }: NodeProps) {
  const text = ((data as { text?: string }).text ?? '').trim()
  return (
    <NodeCard kind="note" title="">
      <div className="pipeline-node__note-text">{text || t('node.note.empty')}</div>
    </NodeCard>
  )
}
