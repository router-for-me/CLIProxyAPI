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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import { createInstance, type i18n } from 'i18next'
import { useState, type ComponentProps } from 'react'
import { I18nextProvider } from 'react-i18next'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import zhTW from '@/i18n/locales/zh-TW.json'
import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { OAuthSection } from '../oauth-section'

const defaults: ComponentProps<typeof OAuthSection>['defaultValues'] = {
  GitHubOAuthEnabled: false,
  GitHubClientId: '',
  GitHubClientSecret: '',
  'discord.enabled': false,
  'discord.client_id': '',
  'discord.client_secret': '',
  'oidc.enabled': false,
  'oidc.display_name': '',
  'oidc.client_id': '',
  'oidc.client_secret': '',
  'oidc.well_known': '',
  'oidc.authorization_endpoint': '',
  'oidc.token_endpoint': '',
  'oidc.user_info_endpoint': '',
  TelegramOAuthEnabled: false,
  'telegram.client_id': '',
  'telegram.client_secret': '',
  LinuxDOOAuthEnabled: false,
  LinuxDOClientId: '',
  LinuxDOClientSecret: '',
  LinuxDOMinimumTrustLevel: '',
  WeChatAuthEnabled: false,
  WeChatServerAddress: '',
  WeChatServerToken: '',
  WeChatAccountQRCodeImageURL: '',
}

let testI18n: i18n

function Fixture(props: { values?: Partial<typeof defaults> }) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  const [client] = useState(
    () => new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  )

  return (
    <I18nextProvider i18n={testI18n}>
      <QueryClientProvider client={client}>
        <div ref={setContainer} />
        <SettingsPageProvider actionsContainer={container}>
          <OAuthSection
            defaultValues={{ ...defaults, ...props.values }}
            serverAddress='https://gateway.example.com'
          />
        </SettingsPageProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )
}

async function renderSettings(values?: Partial<typeof defaults>) {
  const route = createRootRoute({
    component: () => <Fixture values={values} />,
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(<RouterProvider router={router} />)
}

beforeEach(async () => {
  testI18n = createInstance()
  await testI18n.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} }, zh, 'zh-TW': zhTW },
    interpolation: { escapeValue: false },
  })
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

describe('OAuth settings saves', () => {
  it.each([
    [false, 'provider.example.com/discovery'],
    [true, 'provider.example.com/discovery'],
    [true, 'https://provider.example.com/discovery'],
  ])(
    'saves GitHub independently of unchanged OIDC settings (enabled: %s, URL: %s)',
    async (enabled, url) => {
      const discoveryClient = axios.create()
      vi.spyOn(discoveryClient, 'get').mockRejectedValue(
        new Error('OIDC unavailable')
      )
      const discovery = vi
        .spyOn(axios, 'create')
        .mockReturnValue(discoveryClient)
      const errorToast = vi.spyOn(toast, 'error')
      const user = userEvent.setup()
      await renderSettings({ 'oidc.enabled': enabled, 'oidc.well_known': url })
      await user.type(
        screen.getByRole('textbox', { name: 'Client ID' }),
        'github-client'
      )
      await user.click(screen.getByRole('button', { name: 'Save Changes' }))

      await waitFor(() =>
        expect(api.put).toHaveBeenCalledWith('/api/option/', {
          key: 'GitHubClientId',
          value: 'github-client',
        })
      )
      expect(api.put).toHaveBeenCalledTimes(1)
      expect(discovery).not.toHaveBeenCalled()
      expect(errorToast).not.toHaveBeenCalled()
      expect(screen.getByRole('tab', { name: 'GitHub' })).toHaveAttribute(
        'aria-selected',
        'true'
      )
      expect(screen.getByRole('button', { name: 'Reset' })).toBeDisabled()
    }
  )

  it('does not discover OIDC when saving an unchanged form', async () => {
    const discovery = vi.spyOn(axios, 'create')
    const infoToast = vi.spyOn(toast, 'info')
    const user = userEvent.setup()
    await renderSettings({
      'oidc.well_known': 'provider.example.com/discovery',
    })
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    expect(infoToast).toHaveBeenCalledWith('No changes to save')
    expect(discovery).not.toHaveBeenCalled()
    expect(api.put).not.toHaveBeenCalled()
  })

  it('allows disabling OIDC even when its saved discovery URL is invalid', async () => {
    const user = userEvent.setup()
    await renderSettings({
      'oidc.enabled': true,
      'oidc.well_known': 'provider.example.com/discovery',
    })
    await user.click(screen.getByRole('tab', { name: 'OIDC' }))
    await user.click(screen.getByRole('switch', { name: 'Enable OIDC' }))
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/option/', {
        key: 'oidc.enabled',
        value: false,
      })
    )
    expect(api.put).toHaveBeenCalledTimes(1)
  })

  it('opens OIDC and marks an edited invalid URL even when saving from the GitHub tab', async () => {
    const errorToast = vi.spyOn(toast, 'error')
    const user = userEvent.setup()
    await renderSettings()
    await user.click(screen.getByRole('tab', { name: 'OIDC' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Well-Known URL' }),
      'provider.example.com/discovery'
    )
    await user.click(screen.getByRole('tab', { name: 'GitHub' }))
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    const url = await screen.findByRole('textbox', { name: 'Well-Known URL' })
    expect(url).toHaveAttribute('aria-invalid', 'true')
    expect(url).toHaveAccessibleDescription(
      expect.stringContaining(
        'Well-Known URL must start with http:// or https://'
      )
    )
    expect(api.put).not.toHaveBeenCalled()
    expect(errorToast).not.toHaveBeenCalled()
  })

  it('validates the saved discovery URL before enabling OIDC', async () => {
    const user = userEvent.setup()
    await renderSettings({
      'oidc.well_known': 'provider.example.com/discovery',
    })
    await user.click(screen.getByRole('tab', { name: 'OIDC' }))
    await user.click(screen.getByRole('switch', { name: 'Enable OIDC' }))
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    expect(
      screen.getByRole('textbox', { name: 'Well-Known URL' })
    ).toHaveAttribute('aria-invalid', 'true')
    expect(api.put).not.toHaveBeenCalled()
  })

  it('keeps Save disabled during discovery and persists the discovered endpoints', async () => {
    const client = axios.create()
    const response = {
      data: {
        authorization_endpoint: 'https://provider.example.com/authorize',
        token_endpoint: 'https://provider.example.com/token',
        userinfo_endpoint: 'https://provider.example.com/userinfo',
      },
    }
    let finishDiscovery!: (value: typeof response) => void
    const get = vi.spyOn(client, 'get').mockImplementation(
      () =>
        new Promise((resolve) => {
          finishDiscovery = resolve
        })
    )
    vi.spyOn(axios, 'create').mockReturnValue(client)
    const user = userEvent.setup()
    await renderSettings()
    await user.click(screen.getByRole('tab', { name: 'OIDC' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Well-Known URL' }),
      'https://provider.example.com/discovery'
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() =>
      expect(get).toHaveBeenCalledWith('https://provider.example.com/discovery')
    )
    expect(screen.getByRole('button', { name: 'Saving...' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Reset' })).toBeDisabled()
    expect(api.put).not.toHaveBeenCalled()
    finishDiscovery(response)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Reset' })).toBeDisabled()
    )
    await waitFor(() => expect(api.put).toHaveBeenCalledTimes(4))
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'oidc.authorization_endpoint',
      value: response.data.authorization_endpoint,
    })
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'oidc.token_endpoint',
      value: response.data.token_endpoint,
    })
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'oidc.user_info_endpoint',
      value: response.data.userinfo_endpoint,
    })
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
    )
  })

  it.each(['zh', 'zh-TW'])(
    'uses the Well-Known technical term in %s validation',
    async (language) => {
      await testI18n.changeLanguage(language)
      const user = userEvent.setup()
      await renderSettings()
      await user.click(screen.getByRole('tab', { name: 'OIDC' }))
      await user.type(
        screen.getByRole('textbox', { name: 'Well-Known URL' }),
        'invalid'
      )
      await user.click(
        screen.getByRole('button', { name: testI18n.t('Save Changes') })
      )

      const url = screen.getByRole('textbox', { name: 'Well-Known URL' })
      expect(url).toHaveAccessibleDescription(
        expect.stringContaining('Well-Known URL')
      )
      expect(url).not.toHaveAccessibleDescription(
        expect.stringContaining('知名')
      )
    }
  )

  it('keeps edits available for retry when OIDC discovery fails', async () => {
    const client = axios.create()
    const get = vi
      .spyOn(client, 'get')
      .mockRejectedValue(new Error('OIDC unavailable'))
    vi.spyOn(axios, 'create').mockReturnValue(client)
    const errorToast = vi.spyOn(toast, 'error')
    const user = userEvent.setup()
    await renderSettings()
    await user.click(screen.getByRole('tab', { name: 'OIDC' }))
    await user.type(
      screen.getByRole('textbox', { name: 'Well-Known URL' }),
      'https://provider.example.com/discovery'
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))

    await waitFor(() => expect(errorToast).toHaveBeenCalledTimes(1))
    expect(api.put).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Save Changes' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Reset' })).toBeEnabled()
    expect(screen.getByRole('textbox', { name: 'Well-Known URL' })).toHaveValue(
      'https://provider.example.com/discovery'
    )
    get.mockResolvedValue({ data: {} })
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/api/option/', {
        key: 'oidc.well_known',
        value: 'https://provider.example.com/discovery',
      })
    )
  })
})
