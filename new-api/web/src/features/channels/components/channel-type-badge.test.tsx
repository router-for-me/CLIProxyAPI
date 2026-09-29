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
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { ROLE } from '@/lib/roles'
import { useAuthStore } from '@/stores/auth-store'

import { getTaskPluginOptions } from '../api'
import { CHANNEL_TYPE_TASK_PLUGIN } from '../constants'
import { ChannelTypeLogo, TaskPluginChannelBadge } from './channel-type-badge'

vi.mock('../api', () => ({ getTaskPluginOptions: vi.fn() }))
vi.mock('@/lib/lobe-icon', () => ({
  getLobeIcon: (name: string) => <svg data-testid={name} />,
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

const originalAuth = useAuthStore.getState().auth
let client: QueryClient
beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 1, username: 'root', role: ROLE.SUPER_ADMIN },
    },
  })
  vi.mocked(getTaskPluginOptions).mockReset()
})
afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState({ auth: originalAuth })
})

function ChannelTypeHarness(props: { pluginKey?: string }) {
  return (
    <QueryClientProvider client={client}>
      <TaskPluginChannelBadge pluginKey={props.pluginKey} />
    </QueryClientProvider>
  )
}

test('identifies a bound plugin by its metadata and retains the task-plugin type', async () => {
  vi.mocked(getTaskPluginOptions).mockResolvedValue([
    { key: 'incho', name: 'Incho AI', icon: 'text:IA', models: [] },
  ])
  render(<ChannelTypeHarness pluginKey='incho' />)
  expect(await screen.findByText('Incho AI')).toBeInTheDocument()
  expect(screen.getByText('IA')).toBeInTheDocument()
  expect(screen.getByText('Task Plugin')).toBeInTheDocument()
  expect(screen.queryByTestId('OpenAI.Color')).not.toBeInTheDocument()
})

test('keeps the binding key when the plugin is unavailable', async () => {
  vi.mocked(getTaskPluginOptions).mockResolvedValue([])
  render(<ChannelTypeHarness pluginKey='removed-plugin' />)
  expect(await screen.findByText('removed-plugin')).toBeInTheDocument()
  expect(screen.queryByTestId('OpenAI.Color')).not.toBeInTheDocument()
})

test('does not request plugin metadata without bind permission', () => {
  useAuthStore.setState({
    auth: {
      ...originalAuth,
      user: { id: 2, username: 'admin', role: ROLE.ADMIN },
    },
  })
  render(<ChannelTypeHarness pluginKey='incho' />)
  expect(screen.getByText('incho')).toBeInTheDocument()
  expect(getTaskPluginOptions).not.toHaveBeenCalled()
})

test('unbound task channels use a neutral icon while regular providers keep their logo', () => {
  const { rerender } = render(
    <ChannelTypeLogo type={CHANNEL_TYPE_TASK_PLUGIN} />
  )
  expect(screen.queryByTestId('OpenAI.Color')).not.toBeInTheDocument()
  rerender(<ChannelTypeLogo type={1} />)
  expect(screen.getByTestId('OpenAI.Color')).toBeInTheDocument()
})
