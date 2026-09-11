/**
 * SchemaForm renders a configuration form from the JSON Schema a plugin
 * declares in its descriptor, for plugins that ship no configuration UI of
 * their own.
 *
 * It deliberately covers a small, predictable subset: top-level properties of
 * type string (single or multi-line), number, integer, boolean, enum, and
 * arrays of flat objects or of strings. That is enough for every built-in
 * plugin without a UI, and a plugin needing more is expected to bring its own
 * page, as the fuzzy evaluator and the script processor do. Anything the form
 * cannot render stays editable as raw JSON underneath, so no configuration is
 * ever locked away.
 */

import { useEffect, useState } from 'react'

interface JSONSchema {
  type?: string
  title?: string
  description?: string
  format?: string
  enum?: unknown[]
  default?: unknown
  minimum?: number
  maximum?: number
  properties?: Record<string, JSONSchema>
  items?: JSONSchema
  required?: string[]
}

type ConfigValue = Record<string, unknown>

interface SchemaFormProps {
  schemaJSON: string
  value: ConfigValue
  disabled?: boolean
  onChange: (next: ConfigValue) => void
}

export function SchemaForm({ schemaJSON, value, disabled, onChange }: SchemaFormProps) {
  const schema = parseSchema(schemaJSON)
  if (!schema || !schema.properties) {
    return <RawEditor value={value} disabled={disabled} onChange={onChange} />
  }

  const set = (key: string, v: unknown) => {
    const next = { ...value }
    if (v === undefined) delete next[key]
    else next[key] = v
    onChange(next)
  }

  return (
    <div className="schema-form">
      {Object.entries(schema.properties).map(([key, prop]) => (
        <SchemaField
          key={key}
          name={key}
          schema={prop}
          value={value[key]}
          disabled={disabled}
          onChange={v => set(key, v)}
        />
      ))}
      <details className="schema-form__raw">
        <summary>JSON brut</summary>
        <RawEditor value={value} disabled={disabled} onChange={onChange} />
      </details>
    </div>
  )
}

interface SchemaFieldProps {
  name: string
  schema: JSONSchema
  value: unknown
  disabled?: boolean
  onChange: (v: unknown) => void
}

function SchemaField({ name, schema, value, disabled, onChange }: SchemaFieldProps) {
  const label = schema.title ?? name
  const current = value ?? schema.default

  if (schema.enum) {
    return (
      <Field label={label} description={schema.description}>
        <select
          className="pipeline-inspector__input"
          value={current === undefined ? '' : String(current)}
          disabled={disabled}
          onChange={e => onChange(e.target.value === '' ? undefined : e.target.value)}
        >
          <option value="">—</option>
          {schema.enum.map(opt => (
            <option key={String(opt)} value={String(opt)}>
              {String(opt)}
            </option>
          ))}
        </select>
      </Field>
    )
  }

  switch (schema.type) {
    case 'boolean':
      return (
        <label className="schema-form__switch">
          <input
            type="checkbox"
            checked={current === true}
            disabled={disabled}
            onChange={e => onChange(e.target.checked)}
          />
          <span>
            <span className="schema-form__label">{label}</span>
            {schema.description && <div className="schema-form__desc">{schema.description}</div>}
          </span>
        </label>
      )

    case 'number':
    case 'integer':
      return (
        <Field label={label} description={schema.description}>
          <NumberInput
            integer={schema.type === 'integer'}
            min={schema.minimum}
            max={schema.maximum}
            value={current}
            disabled={disabled}
            onChange={onChange}
          />
        </Field>
      )

    case 'array':
      return (
        <Field label={label} description={schema.description}>
          <ArrayField schema={schema.items ?? {}} value={Array.isArray(value) ? value : []} disabled={disabled} onChange={onChange} />
        </Field>
      )

    case 'object':
      return (
        <Field label={label} description={schema.description}>
          <RawEditor value={(value as ConfigValue) ?? {}} disabled={disabled} onChange={onChange} />
        </Field>
      )

    default: {
      const multiline = schema.format === 'multiline'
      const str = current === undefined || current === null ? '' : String(current)
      return (
        <Field label={label} description={schema.description}>
          {multiline ? (
            <textarea
              className="pipeline-inspector__input schema-form__textarea"
              value={str}
              disabled={disabled}
              onChange={e => onChange(e.target.value)}
            />
          ) : (
            <input
              className="pipeline-inspector__input"
              value={str}
              disabled={disabled}
              onChange={e => onChange(e.target.value === '' ? undefined : e.target.value)}
            />
          )}
        </Field>
      )
    }
  }
}

interface ArrayFieldProps {
  schema: JSONSchema
  value: unknown[]
  disabled?: boolean
  onChange: (v: unknown[]) => void
}

/** ArrayField edits a list of flat objects as a table, or a list of strings as rows. */
function ArrayField({ schema, value, disabled, onChange }: ArrayFieldProps) {
  const columns = schema.type === 'object' && schema.properties ? Object.entries(schema.properties) : null

  const update = (idx: number, row: unknown) => onChange(value.map((r, i) => (i === idx ? row : r)))
  const remove = (idx: number) => onChange(value.filter((_, i) => i !== idx))
  const add = () => onChange([...value, columns ? {} : ''])

  return (
    <div className="schema-form__field">
      {value.length > 0 && (
        <table className="schema-form__table">
          {columns && (
            <thead>
              <tr>
                {columns.map(([key, col]) => (
                  <th key={key}>{col.title ?? key}</th>
                ))}
                <th />
              </tr>
            </thead>
          )}
          <tbody>
            {value.map((row, idx) => (
              <tr key={idx}>
                {columns ? (
                  columns.map(([key, col]) => {
                    const obj = (row ?? {}) as ConfigValue
                    if (col.enum) {
                      return (
                        <td key={key}>
                          <select
                            className="pipeline-inspector__input"
                            value={obj[key] === undefined || obj[key] === null ? '' : String(obj[key])}
                            disabled={disabled}
                            onChange={e => update(idx, { ...obj, [key]: e.target.value === '' ? undefined : e.target.value })}
                          >
                            <option value="">—</option>
                            {col.enum.map(opt => (
                              <option key={String(opt)} value={String(opt)}>
                                {String(opt)}
                              </option>
                            ))}
                          </select>
                        </td>
                      )
                    }
                    if (col.type === 'number' || col.type === 'integer') {
                      return (
                        <td key={key}>
                          <NumberInput
                            integer={col.type === 'integer'}
                            min={col.minimum}
                            max={col.maximum}
                            value={obj[key]}
                            disabled={disabled}
                            onChange={v => update(idx, { ...obj, [key]: v })}
                          />
                        </td>
                      )
                    }
                    return (
                      <td key={key}>
                        <input
                          className="pipeline-inspector__input"
                          value={obj[key] === undefined || obj[key] === null ? '' : String(obj[key])}
                          disabled={disabled}
                          onChange={e => update(idx, { ...obj, [key]: e.target.value })}
                        />
                      </td>
                    )
                  })
                ) : (
                  <td>
                    <input
                      className="pipeline-inspector__input"
                      value={row === undefined || row === null ? '' : String(row)}
                      disabled={disabled}
                      onChange={e => update(idx, e.target.value)}
                    />
                  </td>
                )}
                <td>
                  {!disabled && (
                    <button type="button" className="schema-form__row-remove" onClick={() => remove(idx)} title="Retirer">
                      ×
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {!disabled && (
        <button type="button" className="schema-form__add" onClick={add}>
          + Ajouter
        </button>
      )}
    </div>
  )
}

interface NumberInputProps {
  integer?: boolean
  min?: number
  max?: number
  value: unknown
  disabled?: boolean
  onChange: (v: number | undefined) => void
}

/**
 * NumberInput keeps the text as typed while the field has focus. A controlled
 * <input type="number"> bound straight to the parsed value cannot take a
 * decimal: the browser reports "" for an incomplete "0.", the parent stores
 * undefined, the field falls back to its default and the dot is lost, so
 * "0.5" can never be entered. The parsed number is propagated on every
 * keystroke that yields one; the text is only resynced from the value when the
 * field is not being edited.
 */
function NumberInput({ integer, min, max, value, disabled, onChange }: NumberInputProps) {
  const fromValue = value === undefined || value === null ? '' : String(value)
  const [text, setText] = useState(fromValue)
  const [focused, setFocused] = useState(false)

  useEffect(() => {
    if (!focused) setText(fromValue)
  }, [fromValue, focused])

  return (
    <input
      className="pipeline-inspector__input"
      type="number"
      step={integer ? 1 : 'any'}
      min={min}
      max={max}
      value={text}
      disabled={disabled}
      onFocus={() => setFocused(true)}
      onBlur={() => {
        setFocused(false)
        setText(fromValue)
      }}
      onChange={e => {
        const raw = e.target.value
        setText(raw)
        if (raw === '') return onChange(undefined)
        const n = Number(raw)
        if (!Number.isNaN(n)) onChange(n)
      }}
    />
  )
}

function RawEditor({ value, disabled, onChange }: { value: ConfigValue; disabled?: boolean; onChange: (v: ConfigValue) => void }) {
  return (
    <textarea
      className="pipeline-inspector__input schema-form__textarea"
      defaultValue={JSON.stringify(value ?? {}, null, 2)}
      disabled={disabled}
      onBlur={e => {
        try {
          const parsed = JSON.parse(e.target.value || '{}') as ConfigValue
          onChange(parsed)
        } catch {
          // Leave the text as typed: the user is still editing.
        }
      }}
    />
  )
}

function Field({ label, description, children }: { label: string; description?: string; children: React.ReactNode }) {
  return (
    <div className="schema-form__field">
      <div className="schema-form__label">{label}</div>
      {description && <div className="schema-form__desc">{description}</div>}
      {children}
    </div>
  )
}

function parseSchema(json: string | undefined): JSONSchema | null {
  if (!json) return null
  try {
    return JSON.parse(json) as JSONSchema
  } catch {
    return null
  }
}
