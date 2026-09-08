import { useEffect, useState } from 'react'
import { fetchPipelineModels } from '../api'
import type { PipelineModelOption } from '../types'

const FREE_TEXT = '__free__'

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
  const [freeText, setFreeText] = useState(false)

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
  const showFree = freeText || (options !== null && value !== '' && !known) || error !== null

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
            <span>Catalogue indisponible, saisie libre.</span>
          ) : (
            <>
              <span>Saisie libre.</span>
              {options && options.length > 0 && !disabled && (
                <button type="button" className="schema-form__add" onClick={() => setFreeText(false)}>
                  Choisir dans le catalogue
                </button>
              )}
            </>
          )}
        </div>
      </div>
    )
  }

  if (options === null) {
    return <div className="pipeline-inspector__hint">Chargement du catalogue…</div>
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
            setFreeText(true)
            return
          }
          onChange(e.target.value)
        }}
      >
        <option value="">— choisir un modèle —</option>
        {groups.map(([group, items]) => (
          <optgroup key={group} label={group}>
            {items.map(o => (
              <option key={o.proxyName} value={o.proxyName}>
                {o.proxyName}
              </option>
            ))}
          </optgroup>
        ))}
        <option value={FREE_TEXT}>Autre (saisie libre)…</option>
      </select>
      {selected && (
        <div className="pipeline-picker__meta">
          <span className={`pipeline-picker__kind pipeline-picker__kind--${selected.kind}`}>
            {KIND_LABEL[selected.kind]}
          </span>
          {selected.description && <span>{selected.description}</span>}
        </div>
      )}
    </div>
  )
}

const KIND_LABEL: Record<PipelineModelOption['kind'], string> = {
  model: 'modèle',
  virtual: 'virtuel',
  personal: 'personnel',
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
