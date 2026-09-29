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
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { QuotaSettingsSection } from '../quota-settings-section'

function Fixture() {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  return (
    <>
      <div ref={setContainer} />
      <SettingsPageProvider actionsContainer={container}>
        <QuotaSettingsSection
          defaultValues={{
            QuotaForNewUser: 0,
            QuotaForInviter: 0,
            QuotaForInvitee: 0,
            TopUpLink: '',
            quota_setting: {
              enable_free_model_pre_consume: true,
              trust_quota_usd: 10,
              pre_consume_multiplier: 1,
            },
          }}
        />
      </SettingsPageProvider>
    </>
  )
}

async function renderSettings() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({ component: Fixture }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('spinbutton', {
    name: 'Input pre-consume multiplier',
  })
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test.each(['0.5', '1.5', '2.5', '0.0001'])(
  'typing multiplier %s preserves the decimal and saves its numeric value',
  async (value) => {
    const user = userEvent.setup()
    const input = await renderSettings()
    expect(input).toHaveValue(1)
    expect(
      screen.getByRole('spinbutton', {
        name: 'Wallet pre-consume bypass threshold (USD)',
      })
    ).toHaveValue(10)
    await user.clear(input)
    await user.type(input, value)
    await user.tab()
    expect(input).toHaveValue(Number(value))
    expect(input).toBeValid()
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/option/', {
        key: 'quota_setting.pre_consume_multiplier',
        value: Number(value),
      })
    )
  }
)

test.each(['0', '-0.5', ''])(
  'invalid multiplier "%s" shows a field error and prevents saving',
  async (value) => {
    const input = await renderSettings()
    fireEvent.change(input, { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(screen.getByText('Must be greater than 0')).toBeInTheDocument()
    expect(api.put).not.toHaveBeenCalled()
  }
)

test('zero threshold disables bypass and saves as zero', async () => {
  await renderSettings()
  const input = screen.getByRole('spinbutton', {
    name: 'Wallet pre-consume bypass threshold (USD)',
  })
  fireEvent.change(input, { target: { value: '0' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'quota_setting.trust_quota_usd',
      value: 0,
    })
  )
})

test.each(['', '-1'])(
  'invalid threshold "%s" prevents saving',
  async (value) => {
    await renderSettings()
    const input = screen.getByRole('spinbutton', {
      name: 'Wallet pre-consume bypass threshold (USD)',
    })
    fireEvent.change(input, { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(input).toHaveAttribute('aria-invalid', 'true'))
    expect(api.put).not.toHaveBeenCalled()
  }
)
