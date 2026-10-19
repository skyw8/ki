import { expect, test } from 'bun:test'
import { compileForm, getPath, setPath } from '../src/features/settings/schema/model.ts'

test('x-ki-ui compiles sections, cards, hidden fields and testids', () => {
  const form = compileForm({
    type: 'object',
    properties: {
      providerToggles: { type: 'object', 'x-ki-ui': { hidden: true } },
      provider: { type: 'string', enum: ['all', 'deepseek'], 'x-ki-ui': { widget: 'select', label: 'config.provider' } },
      maxResults: { type: 'integer', 'x-ki-ui': { widget: 'number', testid: 'max-results' } },
      codexModel: { type: 'string', 'x-ki-ui': { widget: 'model', providerFilter: 'openai-codex', store: 'bare', thinkingField: 'codexThinking' } },
      codexThinking: { type: 'string', 'x-ki-ui': { hidden: true } },
      fetchContent: { type: 'boolean', 'x-ki-ui': { widget: 'checkbox' } },
    },
    'x-ki-ui': {
      version: 1,
      scroll: true,
      saveTestid: 'example-config-save',
      sections: [
        { id: 'global', title: 'config.globalTitle', columns: 2, fields: ['provider', 'maxResults'], options: ['fetchContent'] },
        {
          id: 'providers',
          layout: 'cards',
          cards: [
            { id: 'codex', toggle: ['providerToggles', 'codex'], fields: ['codexModel'], testid: 'example-provider-codex', toggleTestid: 'example-toggle-codex' },
          ],
        },
      ],
    },
  })

  expect(form.mode).toBe('form')
  expect(form.scroll).toBe(true)
  expect(form.saveTestid).toBe('example-config-save')
  expect(form.fieldsByName.providerToggles.hidden).toBe(true)
  expect(form.fieldsByName.provider.widget).toBe('select')
  expect(form.fieldsByName.maxResults.testid).toBe('max-results')
  expect(form.fieldsByName.codexModel.store).toBe('bare')
  expect(form.fieldsByName.codexModel.thinkingField).toBe('codexThinking')
  expect(form.sections[0].fields).toEqual(['provider', 'maxResults'])
  expect(form.sections[0].options).toEqual(['fetchContent'])
  expect(form.sections[1].cards[0].toggle).toEqual(['providerToggles', 'codex'])
  expect(form.sections[1].cards[0].toggleTestid).toBe('example-toggle-codex')
})

test('x-ki-ui auto-appends unlisted fields and falls back to JSON without properties', () => {
  const form = compileForm({
    type: 'object',
    properties: {
      first: { type: 'string' },
      second: { type: 'boolean' },
    },
    'x-ki-ui': { fields: ['first'] },
  })
  expect(form.fields).toEqual(['first', 'second'])
  expect(form.fieldsByName.second.widget).toBe('switch')

  const raw = compileForm({ type: 'object', properties: {}, 'x-ki-ui': { mode: 'json' } })
  expect(raw.mode).toBe('json')

  const complex = compileForm({ type: 'object', properties: { ignored: { type: 'array', items: { type: 'string' } } } })
  expect(complex.mode).toBe('json')
})

test('nested form paths read and write card toggles without mutating siblings', () => {
  const before = { providerToggles: { codex: true, exa: false }, provider: 'all' }
  const next = setPath(before, ['providerToggles', 'codex'], false)
  expect(getPath(next, ['providerToggles', 'codex'])).toBe(false)
  expect(getPath(next, ['providerToggles', 'exa'])).toBe(false)
  expect(getPath(next, 'provider')).toBe('all')
  expect(before.providerToggles.codex).toBe(true)
})
