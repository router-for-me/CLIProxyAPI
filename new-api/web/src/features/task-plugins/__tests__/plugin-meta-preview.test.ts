/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { describe, expect, test, vi } from 'vitest'

import { parseMarketplaceIndex } from '../lib/marketplace'
import {
  parsePluginMetaPreview,
  resolvePluginMetaPreview,
} from '../lib/plugin-meta-preview'
import type { MarketplacePlugin } from '../types'

const inchoMeta = `export const meta = {
  apiVersion: 1, key: "incho", name: "Incho", version: "1.0.1",
  models: ["incho_music"],
  baseUrl: "https://open.yinchaoyongxian.com",
  protocols: [{name: "openai_responses", supports: ["stream", "sync", "background"]}],
  routes: [
    {method: "POST", path: "/incho/submit/:action", type: "submit", decode: "decodeSubmit"},
    {method: "GET", path: "/incho/fetch/:task_id", type: "query"},
  ],
};`

const plugin: MarketplacePlugin = {
  key: 'incho',
  name: 'Incho',
  latest: '1.0.1',
  versions: [
    { version: '1.0.1', path: 'new.js' },
    { version: '1.0.0', path: 'old.js', baseUrl: 'https://old.example.com' },
  ],
  models: ['latest-model'],
  protocols: ['openai_video'],
  channelTypes: [61],
}

test('reads the selected Incho models, protocols and both native routes without requiring runtime hooks', () => {
  const preview = parsePluginMetaPreview(inchoMeta)
  expect(preview.status).toBe('parsed')
  expect(preview.fields.models).toEqual({
    state: 'value',
    origin: 'source',
    value: ['incho_music'],
  })
  expect(preview.fields.protocols).toEqual({
    state: 'value',
    origin: 'source',
    value: [
      { name: 'openai_responses', supports: ['stream', 'sync', 'background'] },
    ],
  })
  expect(preview.fields.routes).toEqual({
    state: 'value',
    origin: 'source',
    value: [
      { method: 'POST', path: '/incho/submit/:action', type: 'submit' },
      { method: 'GET', path: '/incho/fetch/:task_id', type: 'query' },
    ],
  })
  expect(preview.fields.allowedHosts).toEqual({
    state: 'missing',
    origin: 'source',
  })
  expect(preview.fields.auth).toEqual({ state: 'missing', origin: 'source' })
})

test('supports comments, trailing commas, quoted keys and JavaScript string escapes', () => {
  const preview =
    parsePluginMetaPreview(String.raw`export const /* declaration */ meta /* name */ = /* value */ {
    "models": [/* model */ '\x69ncho_\u006dusic', 'line\nquote\'slash\\',],
    baseUrl: "https://example.com/\u{1F3B5}",
    auth: {type: 'api_key'}, channelTypes: [+61, 0x18],
  };`)
  expect(preview.status).toBe('parsed')
  expect(preview.fields.models).toEqual({
    state: 'value',
    origin: 'source',
    value: ['incho_music', "line\nquote'slash\\"],
  })
  expect(preview.fields.baseUrl).toEqual({
    state: 'value',
    origin: 'source',
    value: 'https://example.com/🎵',
  })
  expect(preview.fields.auth).toEqual({
    state: 'value',
    origin: 'source',
    value: 'api_key',
  })
  expect(preview.fields.channelTypes).toEqual({
    state: 'value',
    origin: 'source',
    value: [61, 24],
  })
})

test('previews TypeSafe model constants used by a read-only validation hook', () => {
  const preview = parsePluginMetaPreview(`
    const MODELS = ['jev-1.13.0', 'jev-latest', 'jev-preview'];
    export const meta = {
      models: MODELS,
      routes: [{method: 'POST', path: '/typesafe/v1/systemone', type: 'submit'}],
    };
    function validate(model) { return MODELS.includes(model); }
  `)
  expect(preview.status).toBe('parsed')
  expect(preview.fields.models).toEqual({
    state: 'value',
    origin: 'source',
    value: ['jev-1.13.0', 'jev-latest', 'jev-preview'],
  })
})

test('resolves earlier local constants and nested aliases without using index hints', () => {
  const preview = parsePluginMetaPreview(`
    const MODEL = 'source-model', MODELS = [MODEL];
    const ALIAS /* comment */ = /* comment */ MODELS;
    const ROUTE = {method: 'POST', path: '/source/submit', type: 'submit', models: ALIAS};
    export const meta = {models: ALIAS, routes: [ROUTE]};
  `)
  expect(preview.status).toBe('parsed')
  const fields = resolvePluginMetaPreview(plugin, plugin.versions[0], preview)
  expect(fields.models).toEqual({
    state: 'value',
    origin: 'source',
    value: ['source-model'],
  })
  expect(fields.routes).toEqual({
    state: 'value',
    origin: 'source',
    value: [
      {
        method: 'POST',
        path: '/source/submit',
        type: 'submit',
        models: ['source-model'],
      },
    ],
  })
})

test.each([
  'let MODELS = ["m"];',
  'export const MODELS = ["m"];',
  'const MODELS = getModels();',
  'const MODELS = LATER; const LATER = ["m"];',
  'const MODELS = ALIAS; const ALIAS = MODELS;',
  'const MODELS = ["m"]; MODELS.push("changed");',
  'const MODELS = ["m"]; MODELS[0] = "changed";',
  'const MODELS = ["m"]; mutate(MODELS);',
  'const MODELS = ["m"]; const ALIAS = MODELS; ALIAS.push("changed");',
  'const MODELS = ["m"]; const BOX = {models: MODELS}; mutate(BOX);',
  'const MODELS = ["m"]; const BOX = {models: MODELS, includes() { this.models.push("changed"); }}; BOX.includes();',
  'const MODELS = ["m"]; export {MODELS};',
  'const MODELS = ["m"]; function expose() { return MODELS; }',
  'const MODELS = ["m"]; function change(MODELS) { MODELS.push("changed"); }',
])('keeps unsafe or non-static model constants unknown: %s', (declaration) => {
  const preview = parsePluginMetaPreview(`
    ${declaration}
    export const meta = {models: MODELS, baseUrl: 'https://example.com'};
  `)
  expect(preview.status).toBe('partial')
  expect(preview.fields.models).toEqual({ state: 'unknown' })
  expect(preview.fields.baseUrl).toEqual({
    state: 'value',
    origin: 'source',
    value: 'https://example.com',
  })
})

test('rejects mutations after metadata initialization through a constant alias', () => {
  const preview = parsePluginMetaPreview(`
    const MODELS = ['m'];
    export const meta = {models: MODELS};
    const ALIAS = MODELS;
    ALIAS.splice(0, 1);
  `)
  expect(preview.status).toBe('partial')
  expect(preview.fields.models).toEqual({ state: 'unknown' })
})

test('bounds repeated constant expansion and keeps unrelated literal fields readable', () => {
  const declarations = ["const LEVEL0 = ['m'];"]
  for (let i = 1; i <= 16; i += 1) {
    declarations.push(`const LEVEL${i} = [LEVEL${i - 1}, LEVEL${i - 1}];`)
  }
  const preview = parsePluginMetaPreview(`
    ${declarations.join('\n')}
    export const meta = {
      routes: [{method: 'POST', path: '/jobs', type: 'submit', extra: LEVEL16}],
      models: ['m'],
    };
  `)
  expect(preview.status).toBe('partial')
  expect(preview.fields.routes).toEqual({ state: 'unknown' })
  expect(preview.fields.models).toEqual({
    state: 'value',
    origin: 'source',
    value: ['m'],
  })
})

describe('unreadable declarations never produce partial lists or execute code', () => {
  test.each([
    'export const meta = { ...external, models: ["m"] };',
    'export const meta = { models: ["m"], [key]: [] };',
    'export const meta = makeMeta();',
    'export const meta = { models: ["m"] }; meta.models.push("changed");',
    'export const meta = { models: ["m"] }; mutate(meta);',
    'export const meta = { models: ["m"] ',
    'const meta = { models: ["m"] }; export {meta};',
    String.raw`const MODELS = ["m"]; M\u004fDELS.push("changed"); export const meta = {models: MODELS};`,
  ])('keeps every field unknown for %s', (source) => {
    const preview = parsePluginMetaPreview(source)
    expect(preview.status).toBe('unavailable')
    expect(
      Object.values(preview.fields).every((field) => field.state === 'unknown')
    ).toBe(true)
  })

  test.each([
    'routes: [{method: "GET", path: "/a", type: "query"}, other]',
    'routes: [{method: "GET", path: "/a", type: "query", ...overrides}]',
    'routes: [{method: "GET", path: "/a", type: "unsupported"}]',
    'routes: [], routes: []',
    'get routes() { throw new Error("must not execute") }',
    'routes: [,]',
  ])('marks only the affected field unknown for %s', (declaration) => {
    const preview = parsePluginMetaPreview(
      `export const meta = {models: ['m'], ${declaration}};`
    )
    expect(preview.status).toBe('partial')
    expect(preview.fields.routes).toEqual({ state: 'unknown' })
    expect(preview.fields.models).toEqual({
      state: 'value',
      origin: 'source',
      value: ['m'],
    })
  })

  test.each([
    'previewProbe(); export const meta = {models: ["m"], routes: previewProbe()};',
    'previewProbe(); const ROUTES = previewProbe(); export const meta = {models: ["m"], routes: ROUTES};',
  ])('does not invoke plugin code: %s', (source) => {
    const probe = vi.fn()
    vi.stubGlobal('previewProbe', probe)
    try {
      const preview = parsePluginMetaPreview(source)
      expect(preview.status).toBe('partial')
      expect(probe).not.toHaveBeenCalled()
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

test('keeps source omissions and empty arrays distinct and does not overwrite them with index values', () => {
  const source = parsePluginMetaPreview(
    'export const meta = {models: [], allowedHosts: []};'
  )
  const resolved = resolvePluginMetaPreview(plugin, plugin.versions[0], source)
  expect(resolved.models).toEqual({
    state: 'value',
    origin: 'source',
    value: [],
  })
  expect(resolved.allowedHosts).toEqual({
    state: 'value',
    origin: 'source',
    value: [],
  })
  expect(resolved.protocols).toEqual({ state: 'missing', origin: 'source' })
})

test('only uses plugin-level index hints for the latest version and marks fallback provenance', () => {
  const source = parsePluginMetaPreview('export const meta = buildMeta();')
  const latest = resolvePluginMetaPreview(plugin, plugin.versions[0], source)
  expect(latest.protocols).toEqual({
    state: 'value',
    origin: 'index',
    value: ['openai_video'],
  })
  const older = resolvePluginMetaPreview(plugin, plugin.versions[1], source)
  expect(older.models.state).toBe('unknown')
  expect(older.protocols.state).toBe('unknown')
  expect(older.channelTypes.state).toBe('unknown')
  expect(older.baseUrl).toEqual({
    state: 'value',
    origin: 'index',
    value: 'https://old.example.com',
  })
})

test('preserves valid protocol declarations and empty model lists from the marketplace index', () => {
  const index = parseMarketplaceIndex({
    indexVersion: 1,
    name: 'test',
    plugins: [plugin, { ...plugin, key: 'empty', models: [], protocols: [] }],
  })
  expect(
    index.plugins.find((entry) => entry.key === 'incho')?.protocols
  ).toEqual(['openai_video'])
  expect(index.plugins.find((entry) => entry.key === 'empty')?.models).toEqual(
    []
  )
  expect(
    index.plugins.find((entry) => entry.key === 'empty')?.protocols
  ).toEqual([])
})
