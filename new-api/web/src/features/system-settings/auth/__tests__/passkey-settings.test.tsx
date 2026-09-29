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
// @vitest-environment-options {"url":"https://console.example.com:8443"}

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance, type i18n } from 'i18next'
import { useState, type ReactNode } from 'react'
import { I18nextProvider } from 'react-i18next'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'
import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import type { PasskeyDomainChange } from '../../types'
import { AuthSettings } from '../index'
import { PasskeySection } from '../passkey-section'

const domainHint =
  'Enter a domain such as example.com or localhost, without a protocol, port or path. Put addresses with ports in Allowed Passkey websites.'

const defaults = {
  'passkey.enabled': true,
  'passkey.rp_display_name': 'Example',
  'passkey.rp_id': '',
  'passkey.origins': 'https://example.com,https://api.example.com',
  'passkey.allow_insecure_origin': false,
  'passkey.user_verification': 'preferred' as const,
  'passkey.attachment_preference': '' as const,
}

function domainResponse(overrides: Partial<PasskeyDomainChange> = {}) {
  return {
    data: {
      success: true,
      data: {
        rp_id: 'example.com',
        legacy_rp_ids: window.location.hostname,
        origins: defaults['passkey.origins'],
        previous_rp_id: window.location.hostname,
        effective_rp_id: 'example.com',
        removed_rp_ids: [],
        affected_credentials: 0,
        unknown_credentials: 0,
        confirmation_required: false,
        removal_confirmation: 'reviewed-domains',
        ...overrides,
      },
    },
  }
}

let testI18n: i18n

function Fixture(props: {
  rpId?: string
  origins?: string
  legacy?: string
  children?: ReactNode
}) {
  const [container, setContainer] = useState<HTMLDivElement | null>(null)
  const [client] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: { retry: false },
          mutations: { retry: false },
        },
      })
  )
  return (
    <I18nextProvider i18n={testI18n}>
      <QueryClientProvider client={client}>
        <div ref={setContainer} />
        <SettingsPageProvider actionsContainer={container}>
          {props.children ?? (
            <PasskeySection
              defaultValues={{
                ...defaults,
                'passkey.rp_id': props.rpId ?? '',
                'passkey.legacy_rp_ids': props.legacy ?? '',
                'passkey.origins': props.origins ?? defaults['passkey.origins'],
              }}
            />
          )}
        </SettingsPageProvider>
      </QueryClientProvider>
    </I18nextProvider>
  )
}

beforeEach(async () => {
  testI18n = createInstance()
  await testI18n.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} }, zh },
    interpolation: { escapeValue: false },
  })
  localStorage.clear()
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: { passkey_rp_id: window.location.hostname } },
  })
})

describe('Passkey website guidance', () => {
  it.each([
    ['', ''],
    ['www.nekoapi.com', 'www.nekoapi.com'],
    ['www.nekoapi.com,old.nekoapi.com', 'www.nekoapi.com\nold.nekoapi.com'],
  ])(
    'loads saved compatible domains "%s" through the authentication settings page',
    async (legacy, displayed) => {
      vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
      vi.mocked(api.get).mockImplementation(async (url) => ({
        data: {
          success: true,
          data:
            url === '/api/option/'
              ? [
                  { key: 'passkey.rp_id', value: 'nekoapi.com' },
                  { key: 'passkey.legacy_rp_ids', value: legacy },
                ]
              : { passkey_rp_id: 'nekoapi.com' },
        },
      }))
      const root = createRootRoute()
      const authenticated = createRoute({
        getParentRoute: () => root,
        id: '_authenticated',
      })
      const route = createRoute({
        getParentRoute: () => authenticated,
        path: 'system-settings/auth/$section',
        component: AuthSettings,
      })
      const router = createRouter({
        routeTree: root.addChildren([authenticated.addChildren([route])]),
        history: createMemoryHistory({
          initialEntries: ['/system-settings/auth/passkey'],
        }),
      })
      render(
        <Fixture>
          <RouterProvider router={router} />
        </Fixture>
      )

      expect(
        await screen.findByRole('textbox', {
          name: 'Compatible Passkey domains',
        })
      ).toHaveValue(displayed)
      expect(
        screen.getByRole('textbox', { name: 'Primary Passkey domain' })
      ).toHaveValue('nekoapi.com')
    }
  )

  it('previews server impact and cancels domain removal without saving other settings', async () => {
    const put = vi.spyOn(api, 'put').mockResolvedValue({
      data: {
        success: true,
        data: {
          rp_id: 'example.com',
          legacy_rp_ids: '',
          origins: defaults['passkey.origins'],
          previous_rp_id: 'example.com',
          effective_rp_id: 'example.com',
          removed_rp_ids: ['www.example.com'],
          affected_credentials: 2,
          unknown_credentials: 7,
          confirmation_required: true,
          removal_confirmation: 'reviewed-impact',
        },
      },
    })
    const user = userEvent.setup()
    render(<Fixture rpId='example.com' legacy='www.example.com' />)
    await user.clear(
      screen.getByRole('textbox', { name: 'Compatible Passkey domains' })
    )
    await user.type(
      screen.getByRole('textbox', { name: 'Passkey display name' }),
      ' changed'
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(
      await within(dialog).findByText(
        'Passkeys known to use removed domains: 2'
      )
    ).toBeVisible()
    expect(
      within(dialog).getByText(
        'Passkeys with an unknown domain that may be affected: 7'
      )
    ).toBeVisible()
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(put).toHaveBeenCalledTimes(1)
    expect(put.mock.calls[0]?.[1]).toMatchObject({ preview: true })
  })
  it('shows the effective domain for a blank field without suggesting a change and restores focus', async () => {
    const put = vi.spyOn(api, 'put')
    const user = userEvent.setup()
    render(<Fixture />)
    const input = screen.getByRole('textbox', {
      name: 'Primary Passkey domain',
    })
    expect(input).toHaveValue('')
    await waitFor(() => expect(input).not.toHaveClass('border-amber-500'))
    expect(input).toHaveAccessibleDescription(
      `${domainHint} The system currently uses: ${window.location.hostname}`
    )
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(
      screen.queryByText(/Passkeys only work on the website/)
    ).not.toBeInTheDocument()
    const domains = screen.getByRole('group', { name: 'Passkey domains' })
    const help = within(within(domains).getByRole('status')).getByRole(
      'button',
      { name: 'Why set this?' }
    )
    help.focus()
    await user.keyboard('{Enter}')
    const dialog = screen.getByRole('dialog', {
      name: 'How to set up Passkey domains',
    })
    expect(dialog).toHaveAccessibleDescription(
      'New Passkeys use the primary domain. Compatible domains keep existing Passkeys working.'
    )
    expect(
      await within(dialog).findByText(
        `The system currently uses: ${window.location.hostname}`
      )
    ).toBeVisible()
    expect(
      within(dialog).queryByText(/For this website, you can enter:/)
    ).not.toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(help).toHaveFocus()
    expect(put).not.toHaveBeenCalled()
  })

  it('preserves an effective parent domain instead of replacing it with the current hostname', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: { success: true, data: { passkey_rp_id: 'example.com' } },
    })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue(
        domainResponse({ previous_rp_id: 'example.com', legacy_rp_ids: '' })
      )
    const user = userEvent.setup()
    render(<Fixture />)
    await user.click(screen.getByRole('button', { name: 'Why set this?' }))
    await user.click(
      await screen.findByRole('button', { name: 'Keep existing domain' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    const input = screen.getByRole('textbox', {
      name: 'Primary Passkey domain',
    })
    expect(input).toHaveValue('example.com')
    expect(input).not.toHaveClass('border-amber-500')
    expect(
      screen.getByRole('textbox', { name: 'Allowed Passkey websites' })
    ).toHaveValue('https://example.com\nhttps://api.example.com')
    expect(put).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(
        '/api/option/passkey/domains',
        {
          rp_id: 'example.com',
          legacy_rp_ids: '',
          origins: defaults['passkey.origins'],
          preview: false,
          removal_confirmation: 'reviewed-domains',
        },
        expect.any(Object)
      )
    )
    expect(put).toHaveBeenCalledTimes(2)
    await user.click(screen.getByRole('button', { name: 'Why set this?' }))
    expect(
      await screen.findByText('The system currently uses: example.com')
    ).toBeVisible()
  })

  it('explains the effect on existing Passkeys and fills an empty website list including the port', async () => {
    const user = userEvent.setup()
    render(<Fixture origins='' />)
    await user.click(screen.getByRole('button', { name: 'Why set this?' }))
    expect(
      screen.getByText(
        'When the primary domain changes, the previous domain is automatically kept in the compatible domains list.'
      )
    ).toBeVisible()
    await user.click(
      await screen.findByRole('button', { name: 'Keep existing domain' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    )
    expect(
      screen.getByRole('textbox', { name: 'Allowed Passkey websites' })
    ).toHaveValue(window.location.origin)
  })

  it.each([
    'unrelated.example.com',
    'ample.com',
    'https://console.example.com',
  ])(
    'marks mismatched domain %s in yellow and clears the warning when corrected',
    async (domain) => {
      const user = userEvent.setup()
      render(<Fixture rpId={window.location.hostname} />)
      const input = screen.getByRole('textbox', {
        name: 'Primary Passkey domain',
      })
      expect(input).not.toHaveClass('border-amber-500')
      await user.clear(input)
      await user.type(input, domain)
      expect(input).toHaveClass(
        'border-amber-500',
        'focus-visible:border-amber-500'
      )
      expect(input).toHaveAccessibleDescription(
        `${domainHint} This domain does not match the current website. Passkeys may not work here.`
      )
      await user.clear(input)
      await user.type(input, window.location.hostname)
      expect(input).not.toHaveClass('border-amber-500')
      expect(
        screen.queryByText(
          'This domain does not match the current website. Passkeys may not work here.'
        )
      ).not.toBeInTheDocument()
    }
  )

  it('keeps a matching parent domain unmarked and explains a missing website beside the website list', async () => {
    render(<Fixture rpId='example.com' />)
    expect(
      screen.getByRole('textbox', { name: 'Primary Passkey domain' })
    ).not.toHaveClass('border-amber-500')
    const websites = screen.getByRole('textbox', {
      name: 'Allowed Passkey websites',
    })
    expect(websites).toHaveAccessibleDescription(
      `This list does not include the current website. Add ${window.location.origin} if users sign in here.`
    )
    const user = userEvent.setup()
    await user.type(websites, `\n${window.location.origin}`)
    expect(websites).toHaveAccessibleDescription(
      'Enter one website address per line, such as https://example.com. Do not include a page path.'
    )
  })

  it('disables filling while saving and enables it after the save finishes', async () => {
    const reply = domainResponse({
      rp_id: window.location.hostname,
      effective_rp_id: window.location.hostname,
      legacy_rp_ids: '',
    })
    let finishSave!: (value: ReturnType<typeof domainResponse>) => void
    vi.spyOn(api, 'put')
      .mockResolvedValueOnce(reply)
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finishSave = resolve
          })
      )
    const user = userEvent.setup()
    render(<Fixture />)
    await user.type(
      screen.getByRole('textbox', { name: 'Primary Passkey domain' }),
      window.location.hostname
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await user.click(screen.getByRole('button', { name: 'Why set this?' }))
    const fill = screen.getByRole('button', { name: 'Keep existing domain' })
    expect(fill).toBeDisabled()
    finishSave(reply)
    await waitFor(() => expect(fill).toBeEnabled())
  })

  it('uses everyday Chinese in the field and explanation dialog', async () => {
    await testI18n.changeLanguage('zh')
    const user = userEvent.setup()
    render(<Fixture />)
    await waitFor(() =>
      expect(screen.getByRole('textbox', { name: '主域名' })).not.toHaveClass(
        'border-amber-500'
      )
    )
    await user.click(screen.getByRole('button', { name: '为什么需要设置？' }))
    const dialog = screen.getByRole('dialog', {
      name: '如何设置通行密钥域名',
    })
    expect(dialog).not.toHaveTextContent(/RP ID|Origins?|依赖方/)
    expect(within(dialog).getByText('主域名')).toBeVisible()
    expect(within(dialog).getByText('兼容的通行密钥域名')).toBeVisible()
    expect(dialog).toHaveTextContent('每行填写一个已有通行密钥使用的域名')
    expect(dialog).toHaveTextContent('无法在 example.com 使用')
    expect(
      within(dialog).getByRole('button', { name: '保留现有域名' })
    ).toBeVisible()
  })

  it('keeps manual setup available when the current system domain cannot be read', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('Offline'))
    const user = userEvent.setup()
    render(<Fixture />)
    await user.click(screen.getByRole('button', { name: 'Why set this?' }))
    expect(
      await screen.findByText(
        'The current setting could not be loaded. You can still enter the website domain yourself.'
      )
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Fill in this website' })
    ).toBeEnabled()
  })

  it('requires confirmation before replacing an automatically resolved domain and lets users cancel', async () => {
    const put = vi.spyOn(api, 'put').mockResolvedValue(domainResponse())
    const user = userEvent.setup()
    render(<Fixture />)
    await user.type(
      screen.getByRole('textbox', { name: 'Primary Passkey domain' }),
      'example.com'
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Change the Passkey domain?',
    })
    expect(dialog).toHaveTextContent(
      `Current domain: ${window.location.hostname}`
    )
    expect(dialog).toHaveTextContent('New domain: example.com')
    expect(put).toHaveBeenCalledTimes(1)
    expect(put.mock.calls[0]?.[1]).toMatchObject({ preview: true })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(put).toHaveBeenCalledTimes(1)
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await user.click(
      await screen.findByRole('button', { name: 'Change domain' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(put).toHaveBeenCalledTimes(3)
    expect(put).toHaveBeenLastCalledWith(
      '/api/option/passkey/domains',
      {
        rp_id: 'example.com',
        legacy_rp_ids: '',
        origins: defaults['passkey.origins'],
        preview: false,
        removal_confirmation: 'reviewed-domains',
      },
      expect.any(Object)
    )
    expect(
      screen.getByRole('textbox', { name: 'Compatible Passkey domains' })
    ).toHaveValue(window.location.hostname)
  })

  it('shows the server resolved default before clearing an explicit domain', async () => {
    const put = vi.spyOn(api, 'put').mockResolvedValue(
      domainResponse({
        rp_id: '',
        previous_rp_id: 'example.com',
        effective_rp_id: window.location.hostname,
        legacy_rp_ids: 'example.com',
      })
    )
    const user = userEvent.setup()
    render(<Fixture rpId='example.com' />)
    await user.clear(
      screen.getByRole('textbox', { name: 'Primary Passkey domain' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Change the Passkey domain?',
    })
    expect(dialog).toHaveTextContent('Current domain: example.com')
    expect(dialog).toHaveTextContent(`New domain: ${window.location.hostname}`)
    expect(put).toHaveBeenCalledTimes(1)
    expect(put.mock.calls[0]?.[1]).toMatchObject({ preview: true })
  })

  it('uses the server preview when the public effective domain could not be loaded', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('Offline'))
    const put = vi.spyOn(api, 'put').mockResolvedValue(domainResponse())
    const user = userEvent.setup()
    render(<Fixture />)
    await user.type(
      screen.getByRole('textbox', { name: 'Primary Passkey domain' }),
      'example.com'
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    expect(await screen.findByRole('alertdialog')).toHaveTextContent(
      `Current domain: ${window.location.hostname}`
    )
    expect(put).toHaveBeenCalledTimes(1)
    expect(put.mock.calls[0]?.[1]).toMatchObject({ preview: true })
  })

  it('keeps a failed domain change available for retry without an unhandled rejection', async () => {
    const errorToast = vi.spyOn(toast, 'error')
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValueOnce(domainResponse())
      .mockRejectedValueOnce(new Error('Offline'))
      .mockResolvedValue(domainResponse())
    const user = userEvent.setup()
    render(<Fixture rpId={window.location.hostname} />)
    const input = screen.getByRole('textbox', {
      name: 'Primary Passkey domain',
    })
    await user.clear(input)
    await user.type(input, 'example.com')
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    await user.click(
      await screen.findByRole('button', { name: 'Change domain' })
    )
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(errorToast).toHaveBeenCalledTimes(1))
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'Change domain' })
      ).toBeEnabled()
    )
    await user.click(screen.getByRole('button', { name: 'Change domain' }))
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(put).toHaveBeenCalledTimes(3)
  })

  it('requires a new confirmation when the affected credential count changes', async () => {
    const errorToast = vi.spyOn(toast, 'error')
    const first = domainResponse({
      previous_rp_id: 'example.com',
      legacy_rp_ids: '',
      removed_rp_ids: ['www.example.com'],
      affected_credentials: 1,
      confirmation_required: true,
    })
    const updated = domainResponse({
      ...first.data.data,
      affected_credentials: 2,
      removal_confirmation: 'updated-impact',
    })
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValueOnce(first)
      .mockResolvedValueOnce({
        data: {
          ...updated.data,
          success: false,
          code: 'PASSKEY_RP_ID_REMOVAL_CONFIRMATION_REQUIRED',
          message: 'Review the updated impact before confirming again.',
        },
      })
      .mockResolvedValueOnce(updated)
    const user = userEvent.setup()
    render(<Fixture rpId='example.com' legacy='www.example.com' />)
    await user.clear(
      screen.getByRole('textbox', { name: 'Compatible Passkey domains' })
    )
    await user.click(screen.getByRole('button', { name: 'Save Changes' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Remove compatible Passkey domains?',
    })
    expect(dialog).toHaveTextContent('Passkeys known to use removed domains: 1')
    await user.click(
      within(dialog).getByRole('button', { name: 'Remove domains' })
    )
    expect(
      await within(dialog).findByText(
        'Passkeys known to use removed domains: 2'
      )
    ).toBeVisible()
    expect(within(dialog).getByRole('alert')).toHaveTextContent(
      'Review the updated impact before confirming again.'
    )
    await waitFor(() =>
      expect(
        within(dialog).getByRole('button', { name: 'Remove domains' })
      ).toBeEnabled()
    )
    await user.click(
      within(dialog).getByRole('button', { name: 'Remove domains' })
    )
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(put).toHaveBeenCalledTimes(3)
    expect(put.mock.calls[2]?.[1]).toMatchObject({
      preview: false,
      removal_confirmation: 'updated-impact',
    })
    expect(errorToast).not.toHaveBeenCalled()
  })
})
