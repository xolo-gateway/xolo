import { useEffect, useState } from 'react'
import { fetchPipelineModels } from '../api'
import type { PipelineModelOption } from '../types'
import { rich, t } from '../i18n'

const FREE_TEXT = '__free__'

/**
 * Which view the user asked for. `auto` lets the value decide: a proxy name
 * the catalog knows is edited through the select, anything else as free text.
 */
type PickerMode = 'auto' | 'free' | 'catalog'

interface ModelPickerProps {
  value: string
  disabled?: boolean
  onChange: (proxyName: string) => void
}

/**
 * ModelPicker offers the models a model_name port can name, grouped by
 * provider, with the org's virtual models after them. A value the catalog does
 * not know (a model of another org, a name typed before the picker existed)
 * is kept and shown as free text rather than silently replaced.
 */
export function ModelPicker({ value, disabled, onChange }: ModelPickerProps) {
  const [options, setOptions] = useState<PipelineModelOption[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [mode, setMode] = useState<PickerMode>('auto')

  useEffect(() => {
    let cancelled = false
    fetchPipelineModels()
      .then(opts => {
        if (!cancelled) setOptions(opts)
      })
      .catch(err => {
        if (!cancelled) setError(String(err))
      })
    return () => {
      cancelled = true
    }
  }, [])

  const known = options?.some(o => o.proxyName === value) ?? false
  const unknownValue = options !== null && value !== '' && !known
  const showFree = error !== null || mode === 'free' || (mode === 'auto' && unknownValue)

  if (showFree) {
    return (
      <div className="pipeline-picker">
        <input
          className="pipeline-inspector__input"
          value={value}
          placeholder="org/gpt-4o"
          disabled={disabled}
          onChange={e => onChange(e.target.value)}
        />
        <div className="pipeline-picker__meta">
          {error ? (
            <span>{t('picker.catalogUnavailable')}</span>
          ) : (
            <>
              <span>{t('picker.freeText')}</span>
              {options && options.length > 0 && !disabled && (
                <button type="button" className="schema-form__add" onClick={() => setMode('catalog')}>
                  {t('picker.chooseFromCatalog')}
                </button>
              )}
            </>
          )}
        </div>
      </div>
    )
  }

  if (options === null) {
    return <div className="pipeline-inspector__hint">{t('picker.loading')}</div>
  }

  const groups = groupBy(options)
  const selected = options.find(o => o.proxyName === value)

  return (
    <div className="pipeline-picker">
      <select
        className="pipeline-inspector__input"
        value={known ? value : ''}
        disabled={disabled}
        onChange={e => {
          if (e.target.value === FREE_TEXT) {
            setMode('free')
            return
          }
          setMode('auto')
          onChange(e.target.value)
        }}
      >
        <option value="">{t('picker.placeholder')}</option>
        {groups.map(([group, items]) => (
          <optgroup key={group} label={group}>
            {items.map(o => (
              <option key={o.proxyName} value={o.proxyName}>
                {o.proxyName}
              </option>
            ))}
          </optgroup>
        ))}
        <option value={FREE_TEXT}>{t('picker.other')}</option>
      </select>
      {unknownValue && (
        <div className="pipeline-picker__meta">
          <span>
            {rich('picker.currentValue', { value: <code>{value}</code> })}
          </span>
          <button type="button" className="schema-form__add" onClick={() => setMode('free')}>
            {t('picker.backToFreeText')}
          </button>
        </div>
      )}
      {selected && (
        <div className="pipeline-picker__meta">
          <span className={`pipeline-picker__kind pipeline-picker__kind--${selected.kind}`}>
            {t(`picker.kind.${selected.kind}`)}
          </span>
          {selected.description && <span>{selected.description}</span>}
        </div>
      )}
    </div>
  )
}

function groupBy(options: PipelineModelOption[]): Array<[string, PipelineModelOption[]]> {
  const map = new Map<string, PipelineModelOption[]>()
  for (const o of options) {
    const key = o.group ?? ''
    const list = map.get(key)
    if (list) list.push(o)
    else map.set(key, [o])
  }
  return [...map.entries()]
}
