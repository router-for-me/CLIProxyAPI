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
import { AxiosError, type AxiosAdapter } from 'axios'
import { afterEach, expect, it, vi } from 'vitest'

import { api, getNotice } from '../api'

const originalAdapter = api.defaults.adapter

afterEach(() => {
  api.defaults.adapter = originalAdapter
  vi.restoreAllMocks()
})

it('a retry after 404 revalidates HTTP caches instead of accepting a stored error', async () => {
  const adapter = vi
    .fn<AxiosAdapter>()
    .mockImplementationOnce(async (config) => {
      throw new AxiosError('Not Found', 'ERR_BAD_REQUEST', config, undefined, {
        data: { error: { message: 'Invalid URL' } },
        status: 404,
        statusText: 'Not Found',
        headers: {},
        config,
      })
    })
    .mockImplementationOnce(async (config) => ({
      data: { success: true, data: { exists: false } },
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }))
  api.defaults.adapter = adapter
  const config = { skipErrorHandler: true }
  await expect(api.get('/api/user/token/status', config)).rejects.toMatchObject(
    { response: { status: 404 } }
  )
  const response = await api.get('/api/user/token/status', config)
  expect(response.data.data.exists).toBe(false)
  expect(adapter).toHaveBeenCalledTimes(2)
  for (const [request] of adapter.mock.calls) {
    const directives = String(request.headers.get('Cache-Control'))
      .split(',')
      .map((value: string) => value.trim())
    expect(directives).toContain('no-cache')
    expect(directives).toContain('no-store')
  }
})

it('public notices keep their existing ETag revalidation policy', async () => {
  const adapter = vi.fn<AxiosAdapter>(async (config) => ({
    data: { success: true },
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }))
  api.defaults.adapter = adapter
  await getNotice()
  expect(adapter.mock.calls[0][0].headers.get('Cache-Control')).toBeNull()
})
