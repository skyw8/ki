export type JsonObject = Record<string, unknown>

export type FieldWidget = 'text' | 'textarea' | 'password' | 'number' | 'select' | 'switch' | 'checkbox' | 'model'

export type WriteCredential = {
  provider: string
  clearOnSuccess?: boolean
  failureMessage?: string
}

export type FieldDef = {
  name: string
  schema: JsonObject
  ui: JsonObject
  widget: FieldWidget
  secret: boolean
  label: string
  hint: string
  placeholder?: string
  configuredPlaceholder?: string
  inputMode?: string
  autoComplete?: string
  spellCheck?: boolean
  required: boolean
  requiredMessage?: string
  invalidMessage?: string
  span: 1 | 2 | 'full'
  testid?: string
  hidden: boolean
  displayScale: number
  displayUnit?: string
  displayMinimum?: number
  displayMaximum?: number
  step?: number
  enumValues: unknown[]
  enumLabels: Record<string, string>
  writeCredential?: WriteCredential
  providerFilter?: string
  thinkingField?: string
  store: 'bare' | 'spec'
  legacyBareProvider?: string
  useDefaultModel: boolean
  clearable: boolean
  clearLabel?: string
  emptyLabel?: string
  emptyDetail?: string
  customDetail?: string
  dialogTitle?: string
  dialogTestid?: string
}

export type CardDef = {
  id: string
  toggle: string[]
  title?: string
  description?: string
  accent?: string
  headerStatus?: string
  link?: { label?: string; text?: string }
  badges: { label?: string; tone?: string }[]
  fields: string[]
  testid?: string
  toggleTestid?: string
}

export type SectionDef = {
  id: string
  title?: string
  description?: string
  columns: number
  fields: string[]
  options: string[]
  layout: 'form' | 'cards'
  cards: CardDef[]
  toggleHint?: string
}

export type CompiledForm = {
  mode: 'form' | 'json'
  version: number
  intro?: string
  scroll: boolean
  columns: number
  fields: string[]
  options: string[]
  sections: SectionDef[]
  fieldsByName: Record<string, FieldDef>
  saveTestid?: string
}

const widgets: FieldWidget[] = ['text', 'textarea', 'password', 'number', 'select', 'switch', 'checkbox', 'model']

export function asObject(value: unknown): JsonObject {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as JsonObject : {}
}

export function asArray(value: unknown): unknown[] {
  return Array.isArray(value) ? value : []
}

export function asString(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

export function asNumber(value: unknown, fallback = 0): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

export function asBoolean(value: unknown): boolean {
  return value === true
}

export function stringList(value: unknown): string[] {
  return asArray(value).filter((item): item is string => typeof item === 'string' && item !== '')
}

function stringMap(value: unknown): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [key, item] of Object.entries(asObject(value))) {
    if (typeof item === 'string') out[key] = item
  }
  return out
}

function metaText(ui: JsonObject, key: string, schema: JsonObject, fallbackKey: string): string {
  const value = ui[key]
  if (value === false) return ''
  return asString(value) || asString(schema[fallbackKey])
}

function inferWidget(schema: JsonObject, ui: JsonObject): FieldWidget {
  const explicit = asString(ui.widget) as FieldWidget
  if (widgets.includes(explicit)) return explicit
  if (schema.secret === true) return 'password'
  const type = asString(schema.type)
  if (type === 'boolean') return 'switch'
  if (type === 'integer' || type === 'number') return 'number'
  if (asArray(schema.enum).length > 0) return 'select'
  return 'text'
}

function compileField(name: string, raw: unknown, required: boolean): FieldDef {
  const schema = asObject(raw)
  const ui = asObject(schema['x-ki-ui'])
  const widget = inferWidget(schema, ui)
  const span = ui.span === 'full' || widget === 'model' ? 'full' : ui.span === 2 ? 2 : 1
  const credential = asObject(ui.writeCredential)
  const provider = asString(credential.provider)
  return {
    name,
    schema,
    ui,
    widget,
    secret: schema.secret === true,
    label: metaText(ui, 'label', schema, 'title') || name,
    hint: metaText(ui, 'hint', schema, 'description'),
    placeholder: asString(ui.placeholder) || undefined,
    configuredPlaceholder: asString(ui.configuredPlaceholder) || undefined,
    inputMode: asString(ui.inputMode) || undefined,
    autoComplete: asString(ui.autoComplete) || undefined,
    spellCheck: typeof ui.spellCheck === 'boolean' ? ui.spellCheck : undefined,
    required: required || ui.required === true,
    requiredMessage: asString(ui.requiredMessage) || undefined,
    invalidMessage: asString(ui.invalidMessage) || undefined,
    span,
    testid: asString(ui.testid) || undefined,
    hidden: ui.hidden === true || asString(ui.widget) === 'hidden',
    displayScale: asNumber(ui.displayScale, 1),
    displayUnit: asString(ui.displayUnit) || undefined,
    displayMinimum: typeof ui.minimum === 'number' ? ui.minimum : undefined,
    displayMaximum: typeof ui.maximum === 'number' ? ui.maximum : undefined,
    step: typeof ui.step === 'number' ? ui.step : asString(schema.type) === 'integer' ? 1 : undefined,
    enumValues: asArray(schema.enum),
    enumLabels: stringMap(ui.enumLabels),
    writeCredential: provider ? {
      provider,
      clearOnSuccess: credential.clearOnSuccess === true,
      failureMessage: asString(credential.failureMessage) || undefined,
    } : undefined,
    providerFilter: asString(ui.providerFilter) || undefined,
    thinkingField: asString(ui.thinkingField) || undefined,
    store: asString(ui.store) === 'bare' ? 'bare' : 'spec',
    legacyBareProvider: asString(ui.legacyBareProvider) || undefined,
    useDefaultModel: ui.useDefaultModel === true,
    clearable: ui.clearable === true,
    clearLabel: asString(ui.clearLabel) || undefined,
    emptyLabel: asString(ui.emptyLabel) || undefined,
    emptyDetail: asString(ui.emptyDetail) || undefined,
    customDetail: asString(ui.customDetail) || undefined,
    dialogTitle: asString(ui.dialogTitle) || undefined,
    dialogTestid: asString(ui.dialogTestid) || undefined,
  }
}

function compileCard(raw: unknown, index: number): CardDef {
  const card = asObject(raw)
  const link = asObject(card.link)
  return {
    id: asString(card.id) || `card-${index}`,
    toggle: stringList(Array.isArray(card.toggle) ? card.toggle : [card.toggle]),
    title: asString(card.title) || undefined,
    description: asString(card.description) || undefined,
    accent: asString(card.accent) || undefined,
    headerStatus: asString(card.headerStatus) || undefined,
    link: Object.keys(link).length > 0 ? { label: asString(link.label), text: asString(link.text) } : undefined,
    badges: asArray(card.badges).map(item => {
      const badge = asObject(item)
      return { label: asString(badge.label) || undefined, tone: asString(badge.tone) || undefined }
    }),
    fields: stringList(card.fields),
    testid: asString(card.testid) || undefined,
    toggleTestid: asString(card.toggleTestid) || undefined,
  }
}

function compileSection(raw: unknown, index: number, fieldsByName: Record<string, FieldDef>, columns: number): SectionDef {
  const section = asObject(raw)
  const cardFields = new Set<string>()
  const cards = asArray(section.cards).map((item, cardIndex) => {
    const card = compileCard(item, cardIndex)
    for (const field of card.fields) cardFields.add(field)
    return card
  })
  return {
    id: asString(section.id) || `section-${index}`,
    title: asString(section.title) || undefined,
    description: asString(section.description) || undefined,
    columns: asNumber(section.columns, columns),
    fields: stringList(section.fields).filter(name => fieldsByName[name] && !cardFields.has(name)),
    options: stringList(section.options).filter(name => fieldsByName[name]),
    layout: asString(section.layout) === 'cards' ? 'cards' : 'form',
    cards,
    toggleHint: asString(section.toggleHint) || undefined,
  }
}

export function compileForm(input: unknown): CompiledForm {
  const schema = asObject(input)
  const properties = asObject(schema.properties)
  const rootUi = asObject(schema['x-ki-ui'])
  const rootRequired = new Set(stringList(schema.required))
  const fieldsByName: Record<string, FieldDef> = {}
  for (const [name, raw] of Object.entries(properties)) {
    fieldsByName[name] = compileField(name, raw, rootRequired.has(name))
  }
  const hasUi = Object.keys(rootUi).length > 0
  const columns = Math.max(1, Math.min(2, asNumber(rootUi.columns, 1)))
  const fields = stringList(rootUi.fields).filter(name => fieldsByName[name])
  const sections = asArray(rootUi.sections).map((item, index) => compileSection(item, index, fieldsByName, columns))
  const used = new Set<string>(fields)
  for (const section of sections) {
    for (const name of section.fields) used.add(name)
    for (const name of section.options) used.add(name)
    for (const card of section.cards) for (const name of card.fields) used.add(name)
  }
  let unsupported = false
  for (const field of Object.values(fieldsByName)) {
    if (field.hidden || used.has(field.name)) continue
    const type = asString(field.schema.type)
    // Complex values are preserved but not guessed into a text input. A schema
    // without explicit x-ki-ui falls back to the JSON editor instead of
    // silently dropping fields it cannot render.
    if (type === 'object' || type === 'array') {
      unsupported = true
      continue
    }
    if (sections.length > 0) sections[sections.length - 1].fields.push(field.name)
    else fields.push(field.name)
  }
  const mode = asString(rootUi.mode) === 'json' || Object.keys(fieldsByName).length === 0 || (!hasUi && unsupported) ? 'json' : 'form'
  return {
    mode,
    version: asNumber(rootUi.version, 1),
    intro: asString(rootUi.intro) || undefined,
    scroll: rootUi.scroll === true,
    columns,
    fields,
    options: stringList(rootUi.options).filter(name => fieldsByName[name]),
    sections,
    fieldsByName,
    saveTestid: asString(rootUi.saveTestid) || undefined,
  }
}

export function pathParts(path: string | string[]): string[] {
  return Array.isArray(path) ? path : path.split('.').filter(Boolean)
}

export function getPath(value: unknown, path: string | string[]): unknown {
  let current: unknown = value
  for (const key of pathParts(path)) {
    if (!current || typeof current !== 'object' || Array.isArray(current)) return undefined
    current = (current as JsonObject)[key]
  }
  return current
}

export function setPath<T extends JsonObject>(value: T, path: string | string[], next: unknown): T {
  const parts = pathParts(path)
  const write = (current: JsonObject, index: number): JsonObject => {
    const key = parts[index]
    const copy: JsonObject = { ...current }
    if (index === parts.length - 1) copy[key] = next
    else copy[key] = write(asObject(current[key]), index + 1)
    return copy
  }
  return write(value, 0) as T
}
