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
import { describe, expect, test } from 'vitest'

import {
  deriveModelMappingPairs,
  mergeModelMappingPairs,
} from '../model-mapping-rules'

describe('deriveModelMappingPairs', () => {
  test('stripping one of several suffixes from upstream names yields request-to-upstream pairs and skips unchanged models', () => {
    expect(
      deriveModelMappingPairs(
        [' gpt-4o-all ', 'claude-sonnet-latest', 'o3', 'gpt-4o-all'],
        'upstream',
        { type: 'strip-suffix', values: '-all, -latest' }
      )
    ).toEqual({
      pairs: [
        { from: 'gpt-4o', to: 'gpt-4o-all' },
        { from: 'claude-sonnet', to: 'claude-sonnet-latest' },
      ],
      unchanged: ['o3'],
      conflicts: [],
    })
  })

  test('stripping a vendor prefix reports models that collapse onto the same request name', () => {
    expect(
      deriveModelMappingPairs(
        ['openai/gpt-4o', 'azure/gpt-4o', 'anthropic/claude'],
        'upstream',
        { type: 'strip-prefix', values: 'openai/,azure/, anthropic/' }
      )
    ).toEqual({
      pairs: [
        { from: 'gpt-4o', to: 'openai/gpt-4o' },
        { from: 'claude', to: 'anthropic/claude' },
      ],
      unchanged: [],
      conflicts: ['azure/gpt-4o'],
    })
  })

  test('request names derive upstream names by adding an affix or replacing text', () => {
    expect(
      deriveModelMappingPairs(['gemini-2.5-flash'], 'request', {
        type: 'add-suffix',
        value: '-all',
      }).pairs
    ).toEqual([{ from: 'gemini-2.5-flash', to: 'gemini-2.5-flash-all' }])
    expect(
      deriveModelMappingPairs(['gpt-4o'], 'request', {
        type: 'add-prefix',
        value: 'openai/',
      }).pairs
    ).toEqual([{ from: 'gpt-4o', to: 'openai/gpt-4o' }])
    expect(
      deriveModelMappingPairs(['claude-3.5-sonnet'], 'upstream', {
        type: 'replace',
        find: '.',
        replaceWith: '-',
      }).pairs
    ).toEqual([{ from: 'claude-3-5-sonnet', to: 'claude-3.5-sonnet' }])
  })

  test('an empty replace search or an affix that strips the whole name produces no pair', () => {
    expect(
      deriveModelMappingPairs(['gpt-4o'], 'request', {
        type: 'replace',
        find: '',
        replaceWith: 'x',
      })
    ).toEqual({ pairs: [], unchanged: ['gpt-4o'], conflicts: [] })
    expect(
      deriveModelMappingPairs(['-all'], 'upstream', {
        type: 'strip-suffix',
        values: '-all',
      })
    ).toEqual({ pairs: [], unchanged: ['-all'], conflicts: [] })
  })
})

describe('mergeModelMappingPairs', () => {
  test('appends new request names and updates existing ones in place', () => {
    expect(
      mergeModelMappingPairs('{"gpt-4o":"old","o3":"o3-all"}', [
        { from: 'gpt-4o', to: 'gpt-4o-all' },
        { from: 'claude', to: 'claude-all' },
      ])
    ).toBe(
      '{\n  "gpt-4o": "gpt-4o-all",\n  "o3": "o3-all",\n  "claude": "claude-all"\n}'
    )
  })

  test('starts from an empty document and returns an empty string when nothing remains', () => {
    expect(mergeModelMappingPairs('  ', [{ from: 'a', to: 'b' }])).toBe(
      '{\n  "a": "b"\n}'
    )
    expect(mergeModelMappingPairs('', [])).toBe('')
  })

  test('refuses to merge into invalid or non-object documents', () => {
    expect(mergeModelMappingPairs('{not json', [{ from: 'a', to: 'b' }])).toBe(
      null
    )
    expect(mergeModelMappingPairs('["a"]', [{ from: 'a', to: 'b' }])).toBe(null)
    expect(mergeModelMappingPairs('{"a":1}', [{ from: 'a', to: 'b' }])).toBe(
      null
    )
  })
})
