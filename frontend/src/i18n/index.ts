import { createElement, Fragment, type ReactElement, type ReactNode } from 'react'
import en from './en.json'
import fr from './fr.json'
import es from './es.json'

/**
 * The editor is localized by the server: the page writes the request language
 * on the mount element (data-locale), and the bundle reads it once. en.json is
 * the source catalog; its keys type every lookup, and fr/es must hold exactly
 * the same keys (enforced by `exact` below).
 */

export type Locale = 'en' | 'fr' | 'es'
export type MessageKey = keyof typeof en
type Catalog = Record<MessageKey, string>

type PluralCategory = Intl.LDMLPluralRule
/** Keys ending in `.other` are plural forms; their base is what `plural` takes. */
export type PluralKey = {
  [K in MessageKey]: K extends `${infer Base}.other` ? Base : never
}[MessageKey]

/** exact rejects a catalog with a key en.json does not have. */
function exact<T extends Catalog>(c: T & Record<Exclude<keyof T, MessageKey>, never>): Catalog {
  return c
}

const catalogs: Record<Locale, Catalog> = { en, fr: exact(fr), es: exact(es) }

const DEFAULT_LOCALE: Locale = 'fr'

function readLocale(): Locale {
  const value = document.getElementById('pipeline-editor-root')?.dataset.locale
  return value === 'en' || value === 'fr' || value === 'es' ? value : DEFAULT_LOCALE
}

export const locale: Locale = readLocale()

const messages = catalogs[locale]
const pluralRules = new Intl.PluralRules(locale)

type Vars = Record<string, string | number>

function interpolate(template: string, vars?: Vars): string {
  if (!vars) return template
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    name in vars ? String(vars[name]) : match
  )
}

/** t returns the message for key in the page locale, with {name} placeholders filled. */
export function t(key: MessageKey, vars?: Vars): string {
  return interpolate(messages[key] ?? catalogs.en[key] ?? key, vars)
}

/**
 * plural picks `<key>.<category>` for count (falling back to `<key>.other`) and
 * fills {count} along with any other placeholder.
 */
export function plural(key: PluralKey, count: number, vars?: Vars): string {
  const category: PluralCategory = pluralRules.select(count)
  const exactKey = `${key}.${category}` as MessageKey
  const chosen = exactKey in messages ? exactKey : (`${key}.other` as MessageKey)
  return t(chosen, { count, ...vars })
}

/**
 * rich renders a message whose placeholders are React nodes (a <code> element,
 * typically), keeping the surrounding sentence in the catalog.
 */
export function rich(key: MessageKey, vars: Record<string, ReactNode>): ReactElement {
  const parts = (messages[key] ?? catalogs.en[key] ?? key)
    .split(/(\{\w+\})/)
    .map(part => {
      const name = /^\{(\w+)\}$/.exec(part)?.[1]
      return name !== undefined && name in vars ? vars[name] : part
    })
  return createElement(Fragment, null, ...parts)
}
