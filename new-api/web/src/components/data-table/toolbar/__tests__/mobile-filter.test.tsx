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
import {
  getCoreRowModel,
  useReactTable,
  type ColumnFiltersState,
} from '@tanstack/react-table'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

import { DataTableToolbar } from '../toolbar'

const rows = [{ name: 'Channel', model: 'model', status: 'enabled' }]
const filters = [
  {
    columnId: 'status',
    title: 'Status',
    options: [{ value: 'enabled', label: 'Enabled' }],
  },
]

function Fixture(props: { collapsibleOnMobile?: boolean }) {
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([
    { id: 'status', value: ['enabled'] },
  ])
  const table = useReactTable({
    data: rows,
    columns: [
      { accessorKey: 'name' },
      { accessorKey: 'model' },
      { accessorKey: 'status' },
    ],
    getCoreRowModel: getCoreRowModel(),
    state: { columnFilters },
    onColumnFiltersChange: setColumnFilters,
  })
  return (
    <DataTableToolbar
      table={table}
      collapsibleOnMobile={props.collapsibleOnMobile}
      searchPlaceholder='Search channels'
      searchDebounceMs={50}
      filters={filters}
      additionalSearch={
        <Input
          aria-label='Model'
          value={String(table.getColumn('model')?.getFilterValue() ?? '')}
          onChange={(event) =>
            table.getColumn('model')?.setFilterValue(event.target.value)
          }
        />
      }
      preActions={<Button>Refresh</Button>}
      viewToggle={<Button>Card view</Button>}
    />
  )
}

function setMobileViewport(mobile: boolean) {
  const original = window.matchMedia
  vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
    ...original(query),
    matches: mobile && query === '(max-width: 640px)',
  }))
}

afterEach(() => {
  vi.restoreAllMocks()
})

it('hides the mobile filter group while keeping reset and quick actions accessible', async () => {
  setMobileViewport(true)
  const user = userEvent.setup()
  render(<Fixture collapsibleOnMobile />)

  const collapse = screen.getByRole('button', { name: 'Collapse' })
  expect(collapse).toHaveAttribute('aria-expanded', 'true')
  expect(screen.getByPlaceholderText('Search channels')).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Model' })).toBeVisible()
  expect(screen.getByRole('button', { name: /^Status/ })).toBeVisible()
  collapse.focus()
  await user.keyboard('{Enter}')

  expect(screen.getByRole('button', { name: 'Expand' })).toHaveAttribute(
    'aria-expanded',
    'false'
  )
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  expect(
    screen.queryByRole('button', { name: /^Status/ })
  ).not.toBeInTheDocument()
  for (const name of ['Reset', 'Refresh', 'Card view', 'View']) {
    expect(screen.getByRole('button', { name })).toBeVisible()
    expect(screen.getByRole('button', { name })).toBeEnabled()
  }
})

it('retains mobile search and filter values after expanding and resets them while collapsed', async () => {
  setMobileViewport(true)
  const user = userEvent.setup()
  render(<Fixture collapsibleOnMobile />)

  await user.type(screen.getByPlaceholderText('Search channels'), 'production')
  await user.type(screen.getByRole('textbox', { name: 'Model' }), 'gpt')
  await user.click(screen.getByRole('button', { name: 'Collapse' }))
  await user.click(screen.getByRole('button', { name: 'Expand' }))

  expect(screen.getByPlaceholderText('Search channels')).toHaveValue(
    'production'
  )
  expect(screen.getByRole('textbox', { name: 'Model' })).toHaveValue('gpt')
  expect(screen.getByRole('button', { name: /^Status/ })).toHaveTextContent(
    'Enabled'
  )
  await user.click(screen.getByRole('button', { name: 'Collapse' }))
  await user.click(screen.getByRole('button', { name: 'Reset' }))
  await user.click(screen.getByRole('button', { name: 'Expand' }))

  expect(screen.getByPlaceholderText('Search channels')).toHaveValue('')
  expect(screen.getByRole('textbox', { name: 'Model' })).toHaveValue('')
  expect(screen.getByRole('button', { name: 'Status' })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: 'Reset' })
  ).not.toBeInTheDocument()
})

it.each([
  { mobile: false, collapsibleOnMobile: true, condition: 'on desktop' },
  { mobile: true, collapsibleOnMobile: undefined, condition: 'without opt-in' },
])('keeps filters visible without a panel toggle $condition', (testCase) => {
  setMobileViewport(testCase.mobile)
  render(<Fixture collapsibleOnMobile={testCase.collapsibleOnMobile} />)

  expect(screen.getByPlaceholderText('Search channels')).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Model' })).toBeVisible()
  expect(screen.getByRole('button', { name: /^Status/ })).toBeVisible()
  expect(
    screen.queryByRole('button', { name: /^(Collapse|Expand)$/ })
  ).not.toBeInTheDocument()
})
