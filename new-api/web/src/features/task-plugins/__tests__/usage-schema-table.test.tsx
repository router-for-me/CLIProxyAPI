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
import { act, render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { afterEach, describe, expect, test } from 'vitest'

import type { BillingUsageSchema } from '@/features/pricing/types'

import { UsageSchemaTable } from '../components/usage-schema-table'

const schema: BillingUsageSchema = {
  duration: {
    type: 'number',
    unit: 'second',
    description: { en: 'Video duration in seconds.', zh: '视频时长(秒)。' },
  },
  resolution: {
    enum: ['480p', '720p', '1080p', '4k'],
    description: { en: 'Output resolution.' },
  },
}

describe('UsageSchemaTable layout', () => {
  afterEach(async () => {
    await act(() => i18next.changeLanguage('en'))
  })

  test('uses localized count unit labels while preserving legacy unit names', async () => {
    render(
      <UsageSchemaTable
        schema={{
          images: {
            type: 'number',
            unit: 'count',
            unitLabel: { en: 'image', zh: '张' },
          },
          legacy: { type: 'number', unit: 'count' },
        }}
      />
    )
    expect(screen.getByRole('cell', { name: 'image' })).toBeVisible()
    expect(screen.getByRole('cell', { name: 'Count' })).toBeVisible()
    await act(() => i18next.changeLanguage('zhCN'))
    expect(screen.getByRole('cell', { name: '张' })).toBeVisible()
    expect(screen.getByRole('cell', { name: i18next.t('Count') })).toBeVisible()
  })

  test('given a usage schema, every declaration renders as a five-column table row', () => {
    render(<UsageSchemaTable schema={schema} />)

    for (const header of [
      'Name',
      'Type',
      'Unit',
      'Enum values',
      'Description',
    ]) {
      expect(
        screen.getByRole('columnheader', { name: header })
      ).toBeInTheDocument()
    }
    expect(
      screen.getByRole('cell', { name: 'Video duration in seconds.' })
    ).toBeInTheDocument()
    expect(
      screen.getByRole('cell', { name: '480p, 720p, 1080p, 4k' })
    ).toBeInTheDocument()
  })

  test('given a description without an English entry, the available locale text still renders', () => {
    render(
      <UsageSchemaTable
        schema={{ note: { description: { zh: '仅中文说明。' } } }}
      />
    )

    expect(
      screen.getByRole('cell', { name: '仅中文说明。' })
    ).toBeInTheDocument()
  })

  test('given a field without a unit, the unit cell falls back to an em dash', () => {
    render(<UsageSchemaTable schema={schema} />)

    const resolutionRow = screen.getByRole('cell', {
      name: 'resolution',
    }).parentElement
    expect(resolutionRow).not.toBeNull()
    expect(resolutionRow?.textContent).toContain('—')
  })
})
