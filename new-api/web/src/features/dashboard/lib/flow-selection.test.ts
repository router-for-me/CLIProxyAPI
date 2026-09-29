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

import type { FlowUserFilterOption } from '../types'
import {
  compactFlowSelectionLabel,
  flowDisplayState,
  requireSuccessfulFlowRows,
  visibleFlowUsers,
} from './flow-selection'

const users: FlowUserFilterOption[] = [
  {
    value: 'user:1',
    label: 'dry',
    valueLabel: '100',
    valueRaw: 100,
    color: '#1664ff',
  },
  {
    value: 'user:2',
    label: 'jrc',
    valueLabel: '70',
    valueRaw: 70,
    color: '#1ac6ff',
  },
]

describe('dashboard flow selection helpers', () => {
  test('limits user chips to currently visible users', () => {
    expect(visibleFlowUsers(users, []).map((user) => user.value)).toEqual([
      'user:1',
      'user:2',
    ])
    expect(
      visibleFlowUsers(users, ['user:2']).map((user) => user.value)
    ).toEqual(['user:2'])
  })

  test('filters visible users without mutating the source options', () => {
    const visible = visibleFlowUsers(users, ['user:1'])

    expect(visible.map((user) => user.value)).toEqual(['user:1'])
    expect(users.map((user) => user.value)).toEqual(['user:1', 'user:2'])
  })

  test('formats compact selected counts for flow multiselect summaries', () => {
    expect(compactFlowSelectionLabel(0)).toBe('*')
    expect(compactFlowSelectionLabel(1)).toBe('1')
    expect(compactFlowSelectionLabel(23)).toBe('23')
  })

  test('prioritizes loading and error states before empty flow data', () => {
    expect(
      flowDisplayState({
        isLoading: true,
        isError: true,
        linkCount: 0,
        themeReady: true,
      })
    ).toBe('loading')
    expect(
      flowDisplayState({
        isLoading: false,
        isError: true,
        linkCount: 0,
        themeReady: true,
      })
    ).toBe('error')
    expect(
      flowDisplayState({
        isLoading: false,
        isError: false,
        linkCount: 0,
        themeReady: true,
      })
    ).toBe('empty')
    expect(
      flowDisplayState({
        isLoading: false,
        isError: false,
        linkCount: 1,
        themeReady: false,
      })
    ).toBe('loading')
  })

  test('throws unsuccessful flow responses instead of treating them as empty data', () => {
    expect(() =>
      requireSuccessfulFlowRows(
        { success: false, data: [], message: 'database unavailable' },
        'Failed to load'
      )
    ).toThrow(/database unavailable/)
    expect(
      requireSuccessfulFlowRows(
        { success: true, data: [{ user_id: 1, quota: 10 }] },
        'Failed to load'
      )
    ).toEqual([{ user_id: 1, quota: 10 }])
  })
})
