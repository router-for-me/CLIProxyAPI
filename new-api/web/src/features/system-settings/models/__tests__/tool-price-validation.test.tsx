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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen } from '@testing-library/react'
import i18next from 'i18next'
import { beforeAll, describe, expect, test } from 'vitest'

import { ToolPriceSettings } from '../tool-price-settings'

describe('tool price validation', () => {
  beforeAll(() => {
    i18next.addResourceBundle('en', 'translation', {
      'Price ($/1K calls)': 'Price ($/1K calls)',
      'Please enter a valid number': 'Please enter a valid number',
      'Tool identifier': 'Tool identifier',
    })
  })

  test('blocks an empty price without converting it to an explicit zero', () => {
    const queryClient = new QueryClient({
      defaultOptions: { mutations: { retry: false } },
    })

    render(
      <QueryClientProvider client={queryClient}>
        <ToolPriceSettings defaultValue='{"web_search":10}' />
      </QueryClientProvider>
    )

    const priceInput = screen.getByRole('spinbutton', {
      name: 'Price ($/1K calls): web_search',
    })
    const saveButton = screen.getByRole('button', { name: 'Save tool prices' })

    fireEvent.change(priceInput, { target: { value: '' } })

    expect(priceInput).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByText('Please enter a valid number')).toBeInTheDocument()
    expect(saveButton).toBeDisabled()

    fireEvent.change(priceInput, { target: { value: '0' } })

    expect(priceInput).toHaveAttribute('aria-invalid', 'false')
    expect(saveButton).toBeEnabled()

    fireEvent.change(priceInput, { target: { value: '0.04' } })

    expect(priceInput).toHaveValue(0.04)
    expect(priceInput).toBeValid()
    expect(saveButton).toBeEnabled()

    fireEvent.change(priceInput, { target: { value: '0.0001' } })
    expect(priceInput).toBeValid()

    fireEvent.change(priceInput, { target: { value: '0.00001' } })
    expect((priceInput as HTMLInputElement).validity.stepMismatch).toBe(true)

    fireEvent.change(priceInput, { target: { value: '-0.04' } })

    expect(priceInput).toHaveAttribute('aria-invalid', 'true')
    expect(saveButton).toBeDisabled()

    queryClient.clear()
  })
})
