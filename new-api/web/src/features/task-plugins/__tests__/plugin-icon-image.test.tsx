import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, waitFor } from '@testing-library/react'
import { AxiosError, type AxiosAdapter } from 'axios'
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
import { StrictMode } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { TaskPluginChannelBadge } from '@/features/channels/components/channel-type-badge'
import { api } from '@/lib/api'
import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { PluginIcon } from '../components/plugin-icon'

vi.mock('@/lib/lobe-icon', () => ({
  getLobeIcon: () => <svg data-testid='lobe-icon' />,
}))

const originalAdapter = api.defaults.adapter
const originalAuth = useAuthStore.getState().auth
const logo = new Blob(['<svg xmlns="http://www.w3.org/2000/svg"/>'], {
  type: 'image/svg+xml',
})
const createObjectURL = vi.fn(() => 'blob:plugin-logo')
const revokeObjectURL = vi.fn()
let client: QueryClient
let adapter: ReturnType<typeof vi.fn<AxiosAdapter>>

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  createObjectURL.mockClear()
  revokeObjectURL.mockClear()
  vi.stubGlobal(
    'URL',
    Object.assign(class extends URL {}, { createObjectURL, revokeObjectURL })
  )
  useAuthStore.setState({
    auth: { ...originalAuth, accessToken: 'test-icon-access-token' },
  })
  adapter = vi.fn(async (config) => ({
    data: logo,
    status: 200,
    statusText: 'OK',
    headers: {},
    config,
  }))
  api.defaults.adapter = adapter
})

afterEach(() => {
  cleanup()
  client.clear()
  api.defaults.adapter = originalAdapter
  useAuthStore.setState({ auth: originalAuth })
  vi.unstubAllGlobals()
})

function GatewayLogo(props: { pluginKey?: string }) {
  return (
    <QueryClientProvider client={client}>
      <PluginIcon
        plugin={{
          key: props.pluginKey ?? 'incho',
          name: props.pluginKey ?? 'Incho',
          hasIcon: true,
        }}
        size={24}
      />
    </QueryClientProvider>
  )
}

describe('PluginIcon image rendering', () => {
  test('loads the plugin logo on first channel render with an empty cache in StrictMode', async () => {
    useAuthStore.setState({
      auth: {
        ...useAuthStore.getState().auth,
        user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
      },
    })
    adapter.mockImplementation(async (config) => ({
      data:
        config.url === '/api/task_plugin_options'
          ? {
              success: true,
              data: [
                { key: 'incho', name: 'Incho', hasIcon: true, models: [] },
              ],
            }
          : logo,
      status: 200,
      statusText: 'OK',
      headers: {},
      config,
    }))
    const { container } = render(
      <StrictMode>
        <QueryClientProvider client={client}>
          <TaskPluginChannelBadge pluginKey='incho' />
        </QueryClientProvider>
      </StrictMode>
    )
    await waitFor(() =>
      expect(container.querySelector('img')?.getAttribute('src')).toBe(
        'blob:plugin-logo'
      )
    )
    expect(container.textContent).toContain('Incho')
    expect(createObjectURL).toHaveBeenCalledWith(logo)
  })

  test('fetches protected logos with the dashboard bearer header and renders a blob image', async () => {
    const { container, unmount } = render(<GatewayLogo />)
    await waitFor(() =>
      expect(container.querySelector('img')?.getAttribute('src')).toBe(
        'blob:plugin-logo'
      )
    )
    expect(adapter).toHaveBeenCalledTimes(1)
    const config = adapter.mock.calls[0][0]
    expect(config.url).toBe('/api/plugin/task/incho/icon')
    expect(config.headers.get('Authorization')).toBe(
      'Bearer test-icon-access-token'
    )
    expect(config.responseType).toBe('blob')
    expect(createObjectURL).toHaveBeenCalledWith(logo)
    expect(container.querySelector('img')?.getAttribute('width')).toBe('24')
    expect(container.querySelector('svg')).toBeNull()
    unmount()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:plugin-logo')
  })

  test.each(['failure', 'non-image'])(
    'keeps the avatar for %s responses',
    async (outcome) => {
      adapter.mockImplementationOnce(async (config) => {
        if (outcome === 'failure') {
          throw new AxiosError('missing logo', undefined, config)
        }
        return {
          data: new Blob(['{}'], { type: 'application/json' }),
          status: 200,
          statusText: 'OK',
          headers: {},
          config,
        }
      })
      const { container } = render(<GatewayLogo />)
      await waitFor(() => expect(client.isFetching()).toBe(0))
      expect(container.querySelector('img')).toBeNull()
      expect(container.textContent).toBe('IN')
      expect(createObjectURL).not.toHaveBeenCalled()
    }
  )

  test('removes the previous plugin logo while a different plugin loads', async () => {
    const { container, rerender } = render(<GatewayLogo />)
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())
    adapter.mockImplementationOnce(() => new Promise(() => {}))
    rerender(<GatewayLogo pluginKey='second' />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toBe('SE')
    await waitFor(() =>
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:plugin-logo')
    )
  })

  test('falls back to text instead of a channel brand when an image cannot be decoded', async () => {
    const { container } = render(
      <PluginIcon
        plugin={{
          key: 'sunoapi',
          name: 'SunoAPI',
          channelTypes: [36],
          iconSrc: 'https://example.com/broken.svg',
        }}
      />
    )
    await waitFor(() => expect(container.querySelector('img')).not.toBeNull())
    fireEvent.error(container.querySelector('img') as HTMLImageElement)
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toBe('SU')
  })

  test('uses public marketplace images directly without authenticated requests', () => {
    const { container } = render(
      <PluginIcon
        plugin={{
          key: 'incho',
          hasIcon: true,
          iconSrc: 'https://example.com/icon.svg',
        }}
      />
    )
    expect(container.querySelector('img')?.getAttribute('src')).toBe(
      'https://example.com/icon.svg'
    )
    expect(adapter).not.toHaveBeenCalled()
  })

  test('ignores inline manifest images and still supports LobeHub icons', () => {
    const { container, rerender, getByTestId } = render(
      <PluginIcon
        plugin={{ key: 'sunoapi', icon: 'data:image/png;base64,iVBORw0KGgo=' }}
      />
    )
    expect(container.querySelector('img')).toBeNull()
    expect(container.textContent).toBe('SU')
    rerender(<PluginIcon plugin={{ key: 'sora', icon: 'Sora.Color' }} />)
    expect(getByTestId('lobe-icon')).toBeTruthy()
  })
})
