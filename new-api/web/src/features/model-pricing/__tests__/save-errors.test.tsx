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
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { AxiosError, type AxiosAdapter } from 'axios'
import { toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/http-client'
import { createAppQueryClient } from '@/lib/query-client'
import { useAuthStore } from '@/stores/auth-store'
import { usePricingPreferencesStore } from '@/stores/pricing-preferences-store'

import type { ModelPricingConfig } from '../api'
import { ModelPricingPanel } from '../model-pricing-panel'
import { pricingOptions } from '../pricing'

const originalAdapter = api.defaults.adapter
let client: QueryClient | undefined

afterEach(() => {
  client?.clear()
  api.defaults.adapter = originalAdapter
  useAuthStore.getState().auth.setUser(null)
  localStorage.clear()
  vi.restoreAllMocks()
})

it.each([200, 400])(
  'shows the backend pricing error once, preserves the draft, and reports a new attempt separately (HTTP %i)',
  async (status) => {
    useAuthStore
      .getState()
      .auth.setUser({ id: 1, username: 'administrator', role: 100 })
    usePricingPreferencesStore.setState({ currency: 'USD' })
    const values = {
      'billing_setting.billing_mode': 'tiered_expr',
      'billing_setting.billing_expr': 'tier("standard", p * 1 + c * 2)',
    }
    const snapshot: ModelPricingConfig = {
      entries: [
        {
          model_name: 'example',
          version: 'v1',
          configured: values,
          effective: values,
        },
      ],
      options: pricingOptions({}),
      empty_version: 'empty',
    }
    const message = 'model_pricing: expression validation failed (1:18)'
    const notify = vi.spyOn(toast, 'error').mockReturnValue('pricing-error')
    const requests: unknown[] = []
    const adapter: AxiosAdapter = async (config) => {
      if (
        config.method === 'get' &&
        ['/api/status', '/api/pricing'].includes(config.url ?? '')
      ) {
        const data =
          config.url === '/api/status'
            ? { success: true, data: {} }
            : { success: true, data: [], vendors: [] }
        return { data, status: 200, statusText: 'OK', headers: {}, config }
      }
      if (
        config.method === 'get' &&
        config.url === '/api/option/model_pricing'
      ) {
        return {
          data: { success: true, data: snapshot },
          status: 200,
          statusText: 'OK',
          headers: {},
          config,
        }
      }
      if (
        config.method === 'patch' &&
        config.url === '/api/option/model_pricing'
      ) {
        requests.push(config.data)
        const response = {
          data: { success: false, message },
          status,
          statusText: 'Error',
          headers: {},
          config,
        }
        if (status === 400) {
          throw new AxiosError(
            'Request failed with status code 400',
            'ERR_BAD_REQUEST',
            config,
            undefined,
            response
          )
        }
        return response
      }
      throw new Error(`Unexpected request: ${config.method} ${config.url}`)
    }
    api.defaults.adapter = adapter
    client = createAppQueryClient()
    render(
      <QueryClientProvider client={client}>
        <ModelPricingPanel modelName='example' />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    const price = await screen.findByRole('textbox', {
      name: 'Input price',
    })
    await user.clear(price)
    await user.type(price, '3')
    const save = screen.getByRole('button', { name: 'Save model prices' })
    await user.click(save)
    await waitFor(() =>
      expect(notify.mock.calls.map(([text]) => text)).toEqual([message])
    )
    expect(await screen.findByRole('alert')).toHaveTextContent(message)
    expect(price).toHaveValue('3')
    await waitFor(() => expect(save).toBeEnabled())
    await user.click(save)
    await waitFor(() =>
      expect(notify.mock.calls.map(([text]) => text)).toEqual([
        message,
        message,
      ])
    )
    expect(requests).toHaveLength(2)
    expect(requests[0]).toContain('p * 3')
  }
)
