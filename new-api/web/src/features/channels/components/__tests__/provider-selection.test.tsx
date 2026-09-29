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
import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { expect, test, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'

import type { TaskPluginOption } from '../../api'
import { ChannelProviderPicker } from '../drawers/channel-provider-picker'

const plugins = [
  {
    key: 'provider-video',
    name: 'OpenAI',
    icon: 'text:PV',
    description: {
      en: 'Video generation via the vendor API',
      zh: '通过厂商接口生成视频',
    },
    models: ['video-model'],
  },
  {
    key: 'another-video',
    name: 'Video Provider',
    icon: 'text:VP',
    models: ['video-one', 'video-two'],
  },
]

const migratedProviders = [
  { type: 36, label: 'SunoAPI', key: 'sunoapi' },
  { type: 50, label: 'Kling', key: 'kling' },
  { type: 51, label: 'Jimeng', key: 'jimeng' },
  { type: 52, label: 'Vidu', key: 'vidu' },
  { type: 54, label: 'Doubao', key: 'doubao' },
  { type: 55, label: 'Sora', key: 'sora' },
]
const migratedPlugins = migratedProviders.map((provider) => ({
  key: provider.key,
  name: `${provider.label} Tasks`,
  icon: 'text',
  models: ['video-model'],
}))

const extensionPlugin: TaskPluginOption = {
  key: 'sora',
  name: 'Sora Video',
  icon: 'text',
  description: { en: 'Video generation', zh: '视频生成' },
  channelTypes: [1, 55],
  models: ['sora-2'],
}

test('creation hides legacy Zhipu from categories and numeric search while GLM remains selectable', async () => {
  const user = userEvent.setup()
  const select = vi.fn()
  render(
    <ChannelProviderPicker
      isCreating
      currentProvider={{ kind: 'builtin', type: 16 }}
      plugins={[]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={select}
    />
  )
  expect(screen.queryByRole('option', { name: /#16$/ })).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Built-in' }))
  const search = screen.getByRole('combobox')
  await user.type(search, 'zhi')
  expect(screen.getAllByRole('option')).toHaveLength(1)
  await user.click(
    screen.getByRole('option', { name: 'Zhipu GLM Built-in #26' })
  )
  expect(select).toHaveBeenCalledWith({ kind: 'builtin', type: 26 })
  await user.clear(search)
  await user.type(search, '16')
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'All' }))
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
})

test('editing an existing legacy Zhipu channel retains its selectable provider', async () => {
  const user = userEvent.setup()
  const select = vi.fn()
  render(
    <ChannelProviderPicker
      isCreating={false}
      currentProvider={{ kind: 'builtin', type: 16 }}
      plugins={[]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={select}
    />
  )
  await user.type(screen.getByRole('combobox'), '16')
  const legacy = screen.getByRole('option', { name: 'Zhipu Built-in #16' })
  expect(legacy).toHaveAttribute('aria-current', 'true')
  await user.click(legacy)
  expect(select).toHaveBeenCalledWith({ kind: 'builtin', type: 16 })
})

test('Zhipu GLM uses the renamed Chinese label and remains searchable after changing language', async () => {
  const language = createInstance()
  await language.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} }, zh },
  })
  const user = userEvent.setup()
  render(
    <I18nextProvider i18n={language}>
      <ChannelProviderPicker
        isCreating
        plugins={[]}
        canBindPlugin
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={vi.fn()}
      />
    </I18nextProvider>
  )
  await user.type(screen.getByRole('combobox'), 'GLM')
  expect(
    screen.getByRole('option', { name: 'Zhipu GLM Built-in #26' })
  ).toBeVisible()
  await act(() => language.changeLanguage('zh'))
  expect(screen.getByRole('option', { name: '智谱GLM 内置 #26' })).toBeVisible()
})

test('plugin extensions keep built-in and independent plugin choices distinct in every category', async () => {
  const select = vi.fn()
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      isCreating
      currentProvider={{ kind: 'builtin', type: 1 }}
      plugins={[extensionPlugin]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={select}
    />
  )
  const builtin = screen.getByRole('option', { name: 'OpenAI Built-in #1' })
  expect(builtin).toHaveClass('min-h-24', 'md:min-h-32')
  expect(builtin).toHaveAttribute('aria-current', 'true')
  const extension = within(builtin).getByLabelText('Plugin extensions')
  expect(extension).toHaveTextContent('Sora Video')
  expect(extension).toHaveClass('text-muted-foreground', 'min-w-0')
  expect(within(extension).getByText('Sora Video')).toHaveClass('truncate')
  expect(extension).toHaveAttribute('title', 'Sora Video · Video generation')
  expect(
    within(builtin).queryByText('Supports plugin extensions')
  ).not.toBeInTheDocument()
  expect(builtin).not.toHaveTextContent('Video generation')
  expect(builtin).toHaveAccessibleDescription(/Sora Video.*Video generation/)
  expect(
    screen.getByRole('option', { name: 'Sora Video Plugin sora' })
  ).toBeVisible()
  expect(
    screen.queryByRole('option', { name: 'Sora Built-in #55' })
  ).not.toBeInTheDocument()
  await user.click(builtin)
  expect(select).toHaveBeenLastCalledWith({ kind: 'builtin', type: 1 })
  await user.click(screen.getByRole('tab', { name: 'Built-in' }))
  expect(
    screen.getByRole('option', { name: 'OpenAI Built-in #1' })
  ).toBeVisible()
  expect(
    screen.queryByRole('option', { name: 'Sora Video Plugin sora' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Plugins' }))
  expect(
    screen.queryByRole('option', { name: 'OpenAI Built-in #1' })
  ).not.toBeInTheDocument()
  await user.click(
    screen.getByRole('option', { name: 'Sora Video Plugin sora' })
  )
  expect(select).toHaveBeenLastCalledWith({ kind: 'plugin', key: 'sora' })
})

test.each(['Sora Video', 'sora'])(
  'searching extension %s finds both its built-in provider and independent plugin',
  async (searchText) => {
    const user = userEvent.setup()
    render(
      <ChannelProviderPicker
        isCreating
        plugins={[extensionPlugin]}
        canBindPlugin
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={vi.fn()}
      />
    )
    await user.type(screen.getByRole('combobox'), searchText)
    expect(screen.getAllByRole('option')).toHaveLength(2)
    expect(
      screen.getByRole('option', { name: 'OpenAI Built-in #1' })
    ).toBeVisible()
    expect(
      screen.getByRole('option', { name: 'Sora Video Plugin sora' })
    ).toBeVisible()
  }
)

test.each([
  { name: 'loading', loading: true, failed: false, canBindPlugin: true },
  { name: 'failed', loading: false, failed: true, canBindPlugin: true },
  { name: 'unpermitted', loading: false, failed: false, canBindPlugin: false },
  {
    name: 'missing metadata',
    loading: false,
    failed: false,
    canBindPlugin: true,
  },
])(
  'built-in providers omit extension claims when metadata is $name',
  (state) => {
    render(
      <ChannelProviderPicker
        isCreating
        plugins={[
          {
            ...extensionPlugin,
            channelTypes:
              state.name === 'missing metadata'
                ? undefined
                : extensionPlugin.channelTypes,
          },
        ]}
        canBindPlugin={state.canBindPlugin}
        loading={state.loading}
        failed={state.failed}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={vi.fn()}
      />
    )
    expect(
      screen.getByRole('option', { name: 'OpenAI Built-in #1' })
    ).toBeVisible()
    expect(screen.queryByLabelText('Plugin extensions')).not.toBeInTheDocument()
  }
)

test('extension associations follow declared types and exclude legacy task-only types', () => {
  render(
    <ChannelProviderPicker
      plugins={[
        { ...extensionPlugin, name: 'OpenAI', channelTypes: [24, 55, 61, 999] },
      ]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  const gemini = screen.getByRole('option', { name: 'Gemini Built-in #24' })
  expect(within(gemini).getByLabelText('Plugin extensions')).toBeVisible()
  for (const name of [
    'OpenAI Built-in #1',
    'Sora Built-in #55',
    'OpenAI Plugin sora',
  ]) {
    expect(
      within(screen.getByRole('option', { name })).queryByLabelText(
        'Plugin extensions'
      )
    ).not.toBeInTheDocument()
  }
})

test('creation replaces migrated built-ins with their plugins in both all and built-in categories', async () => {
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      isCreating
      plugins={migratedPlugins}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  for (const provider of migratedProviders) {
    expect(
      screen.getByRole('option', {
        name: `${provider.label} Tasks Plugin ${provider.key}`,
      })
    ).toBeVisible()
    expect(
      screen.queryByRole('option', {
        name: `${provider.label} Built-in #${provider.type}`,
      })
    ).not.toBeInTheDocument()
  }
  await user.click(screen.getByRole('tab', { name: 'Built-in' }))
  for (const provider of migratedProviders) {
    expect(
      screen.queryByRole('option', {
        name: `${provider.label} Built-in #${provider.type}`,
      })
    ).not.toBeInTheDocument()
  }
})

test.each([
  { label: 'loading', loading: true, failed: false, canBindPlugin: true },
  { label: 'failed', loading: false, failed: true, canBindPlugin: true },
  { label: 'unpermitted', loading: false, failed: false, canBindPlugin: false },
  { label: 'unavailable', loading: false, failed: false, canBindPlugin: true },
])(
  'creation preserves legacy choices when plugins are $label',
  async (state) => {
    const select = vi.fn()
    const user = userEvent.setup()
    render(
      <ChannelProviderPicker
        isCreating
        plugins={state.label === 'unavailable' ? [] : migratedPlugins}
        canBindPlugin={state.canBindPlugin}
        loading={state.loading}
        failed={state.failed}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={select}
      />
    )
    for (const provider of migratedProviders) {
      expect(
        screen.getByRole('option', {
          name: `${provider.label} Built-in #${provider.type}`,
        })
      ).toBeVisible()
    }
    await user.click(screen.getByRole('option', { name: 'Sora Built-in #55' }))
    expect(select).toHaveBeenCalledWith({ kind: 'builtin', type: 55 })
  }
)

test('creation retains complementary built-ins and does not replace a legacy type with an unrelated same-name plugin', () => {
  render(
    <ChannelProviderPicker
      isCreating
      plugins={[
        ...migratedPlugins.filter((plugin) => plugin.key !== 'kling'),
        { key: 'unrelated-kling', name: 'Kling', models: [] },
        ...['google', 'vertex-ai', 'alibaba', 'hailuo'].map((key) => ({
          key,
          name: key,
          models: [],
        })),
      ]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  for (const name of [
    'OpenAI Built-in #1',
    'Gemini Built-in #24',
    'Vertex AI Built-in #41',
    'Ali Built-in #17',
    'MiniMax Built-in #35',
    'VolcEngine Built-in #45',
    'Kling Built-in #50',
    'Kling Plugin unrelated-kling',
  ]) {
    expect(screen.getByRole('option', { name })).toBeVisible()
  }
})

test('creation searches legacy names and translated names through the replacement plugin', async () => {
  const language = createInstance()
  await language.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: {
      en: { translation: {} },
      zh: { translation: { Doubao: '豆包' } },
    },
  })
  const select = vi.fn()
  const user = userEvent.setup()
  render(
    <I18nextProvider i18n={language}>
      <ChannelProviderPicker
        isCreating
        plugins={[
          { key: 'doubao', name: 'Ark Video', models: ['video-model'] },
        ]}
        canBindPlugin
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={select}
      />
    </I18nextProvider>
  )
  const search = screen.getByRole('combobox')
  await user.type(search, 'Doubao')
  expect(
    screen.getByRole('option', { name: 'Ark Video Plugin doubao' })
  ).toBeVisible()
  await act(() => language.changeLanguage('zh'))
  await user.clear(search)
  await user.type(search, '豆包')
  await user.click(screen.getByRole('tab', { name: 'Plugins' }))
  expect(
    screen.getByRole('option', { name: 'Ark Video Plugin doubao' })
  ).toBeVisible()
  await user.click(search)
  await user.keyboard('{ArrowDown}{Enter}')
  expect(select).toHaveBeenCalledWith({ kind: 'plugin', key: 'doubao' })
})

test.each(migratedProviders)(
  'creation searches legacy type $type through its plugin without offering a custom type',
  async (provider) => {
    const select = vi.fn()
    const user = userEvent.setup()
    render(
      <ChannelProviderPicker
        isCreating
        plugins={migratedPlugins}
        canBindPlugin
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={select}
      />
    )
    const search = screen.getByRole('combobox')
    await user.type(search, String(provider.type))
    expect(
      screen.getByRole('option', {
        name: `${provider.label} Tasks Plugin ${provider.key}`,
      })
    ).toBeVisible()
    expect(
      screen.queryByRole('option', {
        name: `${provider.label} Built-in #${provider.type}`,
      })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: 'Custom' }))
    expect(screen.queryByRole('option')).not.toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: 'Plugins' }))
    expect(screen.getAllByRole('option')).toHaveLength(1)
    await user.click(search)
    await user.keyboard('{ArrowDown}{Enter}')
    expect(select).toHaveBeenCalledWith({ kind: 'plugin', key: provider.key })
  }
)

test.each([true, false])(
  'gateway category contains only New API and Sub2API without changing existing categories (plugin permission: %s)',
  async (canBindPlugin) => {
    const user = userEvent.setup()
    const select = vi.fn()
    render(
      <ChannelProviderPicker
        isCreating
        plugins={plugins}
        canBindPlugin={canBindPlugin}
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={select}
      />
    )
    for (const category of ['All', 'Built-in', 'Gateways']) {
      await user.click(screen.getByRole('tab', { name: category }))
      expect(
        screen.getByRole('option', { name: 'New API Built-in #60' })
      ).toBeVisible()
      expect(
        screen.getByRole('option', { name: 'Sub2API Built-in #59' })
      ).toBeVisible()
    }
    expect(screen.getAllByRole('option')).toHaveLength(2)
    await user.click(
      screen.getByRole('option', { name: 'New API Built-in #60' })
    )
    expect(select).toHaveBeenLastCalledWith({ kind: 'builtin', type: 60 })
    const search = screen.getByRole('combobox')
    await user.type(search, 'Sub2API')
    expect(screen.getAllByRole('option')).toHaveLength(1)
    await user.click(
      screen.getByRole('option', { name: 'Sub2API Built-in #59' })
    )
    expect(select).toHaveBeenLastCalledWith({ kind: 'builtin', type: 59 })
    await user.click(screen.getByRole('tab', { name: 'Built-in' }))
    expect(search).toHaveValue('Sub2API')
    expect(
      screen.getByRole('option', { name: 'Sub2API Built-in #59' })
    ).toBeVisible()
    await user.click(screen.getByRole('tab', { name: 'Gateways' }))
    await user.clear(search)
    await user.type(search, '999')
    expect(screen.queryByRole('option')).not.toBeInTheDocument()
  }
)

test('custom providers have their own tab and keep the search when switching categories', async () => {
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={plugins}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  const tabs = screen.getByRole('tablist', { name: 'Provider source' })
  expect(within(tabs).getAllByRole('tab')).toHaveLength(5)
  await user.click(within(tabs).getByRole('tab', { name: 'Custom' }))
  const panel = screen.getByRole('tabpanel', { name: 'Custom' })
  expect(within(panel).getAllByRole('option')).toHaveLength(2)
  expect(
    within(panel).getByRole('option', { name: 'Advanced Custom Built-in #58' })
  ).toBeVisible()
  expect(
    within(panel).getByRole('option', { name: 'Custom Built-in #8' })
  ).toBeVisible()
  expect(
    within(panel).queryByRole('option', { name: /Plugin/ })
  ).not.toBeInTheDocument()

  await user.click(within(tabs).getByRole('tab', { name: 'Built-in' }))
  expect(
    screen.getByRole('option', { name: 'OpenAI Built-in #1' })
  ).toBeVisible()
  expect(
    screen.queryByRole('option', { name: 'Advanced Custom Built-in #58' })
  ).not.toBeInTheDocument()
  expect(
    screen.queryByRole('option', { name: 'Custom Built-in #8' })
  ).not.toBeInTheDocument()
  await user.type(screen.getByRole('combobox'), '58')
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
  await user.click(within(tabs).getByRole('tab', { name: 'Custom' }))
  expect(screen.getByRole('combobox')).toHaveValue('58')
  expect(screen.getAllByRole('option')).toHaveLength(1)
  expect(
    screen.getByRole('option', { name: 'Advanced Custom Built-in #58' })
  ).toBeVisible()
})

test('category tabs use arrow and Enter navigation without moving focus into the search field', async () => {
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={plugins}
      canBindPlugin
      loading={false}
      failed
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  const all = screen.getByRole('tab', { name: 'All' })
  const builtin = screen.getByRole('tab', { name: 'Built-in' })
  await user.click(all)
  await user.keyboard('{ArrowRight}')
  expect(builtin).toHaveFocus()
  expect(all).toHaveAttribute('aria-selected', 'true')
  await user.keyboard('{Enter}')
  expect(builtin).toHaveAttribute('aria-selected', 'true')
  expect(builtin).toHaveFocus()
  expect(screen.getByRole('tabpanel', { name: 'Built-in' })).toHaveAttribute(
    'id',
    builtin.getAttribute('aria-controls')
  )
  expect(screen.queryByText('Failed to load plugins')).not.toBeInTheDocument()

  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'Plugins' })).toHaveFocus()
  expect(screen.getByText('Failed to load plugins')).toBeVisible()
  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'Gateways' })).toHaveFocus()
  expect(screen.getByRole('tabpanel', { name: 'Gateways' })).toBeVisible()
  expect(screen.queryByText('Failed to load plugins')).not.toBeInTheDocument()
  await user.keyboard('{ArrowRight}{Enter}')
  expect(screen.getByRole('tab', { name: 'Custom' })).toHaveFocus()
  expect(screen.getByRole('tabpanel', { name: 'Custom' })).toBeVisible()
  expect(screen.queryByText('Failed to load plugins')).not.toBeInTheDocument()
})

test('removing plugin binding permission returns an active plugin tab to all providers', async () => {
  const user = userEvent.setup()
  const props = {
    plugins,
    loading: false,
    failed: false,
    disabled: false,
    onRetry: vi.fn(),
    onSelect: vi.fn(),
  }
  const view = render(<ChannelProviderPicker {...props} canBindPlugin />)
  await user.click(screen.getByRole('tab', { name: 'Plugins' }))
  view.rerender(<ChannelProviderPicker {...props} canBindPlugin={false} />)
  expect(screen.queryByRole('tab', { name: 'Plugins' })).not.toBeInTheDocument()
  expect(screen.getByRole('tab', { name: 'All' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(screen.getByRole('tabpanel', { name: 'All' })).toBeVisible()
  expect(
    screen.queryByRole('option', { name: /Plugin/ })
  ).not.toBeInTheDocument()
  expect(
    screen.getByRole('option', { name: 'Advanced Custom Built-in #58' })
  ).toBeVisible()
})

test('every built-in provider has a description and Anthropic includes compatible services', async () => {
  const language = createInstance()
  await language.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} }, zhCN: zh },
  })
  render(
    <I18nextProvider i18n={language}>
      <ChannelProviderPicker
        plugins={[]}
        canBindPlugin={false}
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={vi.fn()}
      />
    </I18nextProvider>
  )

  for (const option of screen.getAllByRole('option')) {
    expect(option).toHaveAccessibleDescription()
  }
  const anthropic = screen.getByRole('option', {
    name: 'Anthropic Built-in #14',
  })
  expect(anthropic).toHaveAccessibleDescription(
    'Connect to the Anthropic API or compatible services'
  )
  await act(() => language.changeLanguage('zhCN'))
  expect(anthropic).toHaveAccessibleDescription(
    '接入 Anthropic API 或兼容其接口的服务'
  )
  expect(
    within(anthropic).getByText('接入 Anthropic API 或兼容其接口的服务')
  ).toBeVisible()
})

test('deprecated and flexible integration badges preserve provider selection and expose the full explanation', async () => {
  const select = vi.fn()
  const user = userEvent.setup()
  const props = {
    plugins: [],
    canBindPlugin: false,
    loading: false,
    failed: false,
    disabled: false,
    onRetry: vi.fn(),
    onSelect: select,
  }
  const view = render(<ChannelProviderPicker {...props} />)
  const custom = screen.getByRole('option', { name: 'Custom Built-in #8' })
  const advanced = screen.getByRole('option', {
    name: 'Advanced Custom Built-in #58',
  })
  expect(custom).toHaveAccessibleDescription(
    'Deprecated · Legacy full-URL integration; use Advanced Custom for new channels'
  )
  await user.click(within(custom).getByText('Deprecated'))
  expect(select).toHaveBeenNthCalledWith(1, { kind: 'builtin', type: 8 })

  const details =
    "New API's flexible channel lets you configure upstream addresses and authentication per endpoint, choose native forwarding or supported protocol conversions, and configure model listing and balance queries independently"
  expect(advanced).toHaveAccessibleDescription(
    `Flexible integration · ${details}`
  )
  expect(
    within(advanced).getByText(
      'Configure endpoint routing, authentication and protocol conversion for different upstream services'
    )
  ).toHaveAttribute('title', details)
  await user.click(within(advanced).getByText('Flexible integration'))
  expect(select).toHaveBeenNthCalledWith(2, { kind: 'builtin', type: 58 })

  view.rerender(<ChannelProviderPicker {...props} disabled />)
  expect(advanced).toHaveAttribute('aria-disabled', 'true')
  await user.click(within(advanced).getByText('Flexible integration'))
  expect(select).toHaveBeenCalledTimes(2)
})

test('plugin descriptions follow the current language with an English fallback and remain selectable', async () => {
  const language = createInstance()
  await language.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} } },
  })
  const select = vi.fn()
  const user = userEvent.setup()
  render(
    <I18nextProvider i18n={language}>
      <ChannelProviderPicker
        plugins={plugins}
        canBindPlugin
        loading={false}
        failed={false}
        disabled={false}
        onRetry={vi.fn()}
        onSelect={select}
      />
    </I18nextProvider>
  )

  const option = screen.getByRole('option', {
    name: 'OpenAI Plugin provider-video',
  })
  expect(
    within(option).getByText('Video generation via the vendor API')
  ).toBeVisible()
  expect(option).toHaveAccessibleDescription(
    'Video generation via the vendor API'
  )
  expect(
    screen.getByRole('option', { name: 'Video Provider Plugin another-video' })
  ).not.toHaveAccessibleDescription()

  await act(() => language.changeLanguage('zhCN'))
  expect(within(option).getByText('通过厂商接口生成视频')).toBeVisible()
  expect(option).toHaveAccessibleDescription('通过厂商接口生成视频')
  await act(() => language.changeLanguage('fr'))
  await user.click(
    within(option).getByText('Video generation via the vendor API')
  )
  expect(select).toHaveBeenCalledWith({ kind: 'plugin', key: 'provider-video' })
})

test('built-in and plugin providers with the same name remain distinct and plugin search selects the binding', async () => {
  const select = vi.fn()
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={plugins}
      currentProvider={{ kind: 'builtin', type: 1 }}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={select}
    />
  )
  expect(screen.getAllByRole('option', { name: /^OpenAI / })).toHaveLength(2)
  expect(
    screen.getByRole('option', { name: 'OpenAI Built-in #1' })
  ).toHaveAttribute('aria-current', 'true')
  expect(
    screen.getByRole('option', { name: 'OpenAI Plugin provider-video' })
  ).not.toHaveAttribute('aria-current')
  await user.click(screen.getByRole('tab', { name: 'Plugins' }))
  expect(screen.getAllByRole('option')).toHaveLength(2)
  await user.type(screen.getByRole('combobox'), 'provider-video')
  expect(screen.getAllByRole('option')).toHaveLength(1)
  await user.keyboard('{ArrowDown}{Enter}')
  expect(select).toHaveBeenCalledWith({ kind: 'plugin', key: 'provider-video' })
})

test('searching a known type number selects that type and an unknown positive number remains usable', async () => {
  const select = vi.fn()
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={[]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={select}
    />
  )
  const search = screen.getByRole('combobox')
  await user.type(search, '43')
  await user.keyboard('{ArrowDown}{Enter}')
  expect(select).toHaveBeenLastCalledWith({ kind: 'builtin', type: 43 })
  await user.clear(search)
  await user.type(search, '999')
  await user.click(screen.getByRole('tab', { name: 'Built-in' }))
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Custom' }))
  await user.click(search)
  await user.keyboard('{ArrowDown}{Enter}')
  expect(select).toHaveBeenLastCalledWith({ kind: 'builtin', type: 999 })
  await user.clear(search)
  await user.type(search, '61')
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
})

test('an empty plugin filter reports that no plugins can be bound', async () => {
  const user = userEvent.setup()
  render(
    <ChannelProviderPicker
      plugins={[]}
      canBindPlugin
      loading={false}
      failed={false}
      disabled={false}
      onRetry={vi.fn()}
      onSelect={vi.fn()}
    />
  )
  await user.click(screen.getByRole('tab', { name: 'Plugins' }))
  expect(screen.getByText('No plugins available for binding')).toBeVisible()
  expect(screen.queryByRole('option')).not.toBeInTheDocument()
})
