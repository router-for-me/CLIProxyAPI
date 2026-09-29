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
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createRef } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { evaluateBillingExpression } from '@/features/pricing/lib/billing-expression/runtime'
import { tryParseTaskVisualConfig } from '@/features/pricing/lib/task-expr'
import { tryParseVisualConfig } from '@/features/pricing/lib/tier-expr'
import type { BillingUsageSchema } from '@/features/pricing/types'
import {
  ModelPricingEditorPanel,
  type ModelPricingEditorPanelHandle,
  type ModelRatioData,
} from '@/features/system-settings/models/model-pricing-sheet'
import { api } from '@/lib/api'
import { usePricingPreferencesStore } from '@/stores/pricing-preferences-store'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { previewModelPricing } from '../api'

const clients: QueryClient[] = []

beforeEach(() => {
  localStorage.clear()
  usePricingPreferencesStore.setState({ currency: 'USD' })
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      usdExchangeRate: 7,
    },
  })
  vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: [], vendors: [] },
  })
})

afterEach(() => {
  cleanup()
  clients.splice(0).forEach((client) => client.clear())
  usePricingPreferencesStore.setState({ currency: 'USD' })
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  localStorage.clear()
})

it('reads preview prices and billing metadata from the common data envelope', async () => {
  const effective = { ModelRatio: 0, CompletionRatio: 2 }
  const billingDetails = { audio_input_price: 1 }
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        effective,
        cache_write_mode: 'none',
        billing_details: billingDetails,
      },
    },
  })
  await expect(
    previewModelPricing({ model_name: 'gemini-2.5-flash', pricing: effective })
  ).resolves.toEqual({ effective, cacheWriteMode: 'none', billingDetails })
})

function renderEditor(
  data: Partial<ModelRatioData> = {},
  usageSchema?: BillingUsageSchema
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client.setQueryData(['status'], null)
  clients.push(client)
  const ref = createRef<ModelPricingEditorPanelHandle>()
  const dirty = vi.fn()
  const view = (entry: Partial<ModelRatioData>) => (
    <QueryClientProvider client={client}>
      <ModelPricingEditorPanel
        ref={ref}
        editData={{
          name: 'currency-model',
          billingMode: 'per-token',
          ratio: '1',
          completionRatio: '2',
          ...entry,
        }}
        usageSchema={usageSchema}
        onDirtyChange={dirty}
      />
    </QueryClientProvider>
  )
  const result = render(view(data))
  return {
    ...result,
    ref,
    dirty,
    reload: (entry: Partial<ModelRatioData>) => result.rerender(view(entry)),
  }
}

async function selectCurrency(label: string) {
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Pricing currency' }))
  await user.click(await screen.findByRole('option', { name: label }))
}

async function commit(
  ref: React.RefObject<ModelPricingEditorPanelHandle | null>
) {
  let result: ModelRatioData | null = null
  await act(async () => {
    result = (await ref.current?.commitDraft()) ?? null
  })
  return result as ModelRatioData | null
}

it('previews legacy conversion in the selected currency and applies only after confirmation', async () => {
  const expression = 'tier("base", p * 2 + c * 4 + cr * 0)'
  const effective = {
    ModelRatio: 1,
    CompletionRatio: 2,
    CacheRatio: 0,
    CreateCacheRatio: 1.25,
    ImageRatio: 1,
  }
  const preview = vi.spyOn(api, 'post').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/convert')
        ? { expression, effective, cache_write_mode: 'none' }
        : { effective, cache_write_mode: 'none' },
    },
  }))
  const save = vi.spyOn(api, 'patch')
  const editor = renderEditor({ cacheRatio: '0' })
  await selectCurrency('Site currency (CNY)')
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Convert to expression' }))
  let dialog = await screen.findByRole('alertdialog', {
    name: 'Preview pricing conversion',
  })
  const before = within(dialog).getByRole('region', {
    name: 'Before conversion',
  })
  const after = within(dialog).getByRole('region', {
    name: 'After conversion',
  })
  expect(within(before).getAllByText(/¥14/).length).toBeGreaterThan(0)
  expect(within(after).getAllByText(/¥14/).length).toBeGreaterThan(0)
  expect(within(before).getByText(/^¥0 /)).toBeVisible()
  expect(within(after).getByText(/^¥0 /)).toBeVisible()
  expect(within(after).getByText(expression)).toBeVisible()
  expect(
    within(before).queryByText('Image input price')
  ).not.toBeInTheDocument()
  expect(within(after).queryByText('Image input price')).not.toBeInTheDocument()
  expect(within(dialog).queryByRole('combobox')).not.toBeInTheDocument()
  expect(
    within(before).queryByText('Cache write price')
  ).not.toBeInTheDocument()
  expect(
    within(after).queryByText('Cache create price')
  ).not.toBeInTheDocument()
  expect(
    within(dialog).queryByText('Cache create (1h) price')
  ).not.toBeInTheDocument()
  const original = await commit(editor.ref)
  expect(original).toMatchObject({
    billingMode: 'per-token',
    ratio: '1',
    cacheRatio: '0',
  })
  expect(editor.dirty).toHaveBeenLastCalledWith(false)
  await userEvent
    .setup()
    .click(within(dialog).getByRole('button', { name: 'Cancel' }))
  expect(await commit(editor.ref)).toEqual(original)
  expect(save).not.toHaveBeenCalled()
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Convert to expression' }))
  dialog = await screen.findByRole('alertdialog', {
    name: 'Preview pricing conversion',
  })
  await userEvent
    .setup()
    .click(within(dialog).getByRole('button', { name: 'Apply to draft' }))
  await waitFor(() =>
    expect(screen.getByRole('tab', { name: 'Expression' })).toHaveAttribute(
      'aria-selected',
      'true'
    )
  )
  expect(preview).toHaveBeenCalledWith('/api/option/model_pricing/convert', {
    model_name: 'currency-model',
    pricing: {
      'billing_setting.billing_mode': 'ratio',
      ModelRatio: 1,
      CompletionRatio: 2,
      CacheRatio: 0,
    },
  })
  expect(save).not.toHaveBeenCalled()
  expect(await commit(editor.ref)).toMatchObject({
    billingMode: 'tiered_expr',
    billingExpr: expression,
    ratio: '1',
    cacheRatio: '0',
  })
  await userEvent
    .setup()
    .click(screen.getByRole('tab', { name: 'Per-token (deprecated)' }))
  expect(
    screen.getByRole('button', { name: 'Convert to expression' })
  ).toBeDisabled()
})

it.each([
  {
    name: 'custom-cache',
    ratio: 0,
    mode: 'standard',
    expression: 'tier("base", p * 2 + c * 4 + cr * 1 + cc * 0)',
  },
  {
    name: 'custom-claude',
    ratio: 1.5,
    mode: 'claude_ttl',
    expression: 'tier("base", p * 2 + c * 4 + cr * 1 + cc * 3 + cc1h * 4.8)',
  },
])(
  'keeps configured cache prices visible on both sides for $name',
  async (fixture) => {
    const effective = {
      ModelRatio: 1,
      CompletionRatio: 2,
      CacheRatio: 0.5,
      CreateCacheRatio: fixture.ratio,
      ImageRatio: 1,
    }
    vi.spyOn(api, 'post').mockImplementation(async (url) => ({
      data: {
        success: true,
        data: url.endsWith('/convert')
          ? {
              effective,
              expression: fixture.expression,
              cache_write_mode: fixture.mode,
            }
          : { effective, cache_write_mode: fixture.mode },
      },
    }))
    const editor = renderEditor({
      name: fixture.name,
      createCacheRatio: String(fixture.ratio),
    })
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Convert to expression' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Preview pricing conversion',
    })
    for (const side of ['Before conversion', 'After conversion']) {
      const panel = within(dialog).getByRole('region', { name: side })
      let label =
        side === 'Before conversion'
          ? 'Cache write price'
          : 'Cache create price'
      if (fixture.mode === 'claude_ttl') label = 'Cache Creation (5m)'
      expect(
        within(panel).getByText(label).nextElementSibling
      ).toHaveTextContent(`$${fixture.ratio * 2} / 1M token`)
      if (fixture.mode === 'claude_ttl') {
        expect(
          within(panel).getByText('Cache create (1h) price').nextElementSibling
        ).toHaveTextContent('$4.8 / 1M token')
      } else {
        expect(
          within(panel).queryByText('Cache create (1h) price')
        ).not.toBeInTheDocument()
      }
    }
    await userEvent
      .setup()
      .click(within(dialog).getByRole('button', { name: 'Apply to draft' }))
    expect(await commit(editor.ref)).toMatchObject({
      billingExpr: fixture.expression,
      createCacheRatio: String(fixture.ratio),
    })
  }
)

it.each([
  {
    name: 'gpt-4-32k',
    inputRatio: 30,
    cacheRatio: 1,
    expression: 'tier("base", p * 60 + c * 120)',
  },
  {
    name: 'free-cache-model',
    inputRatio: 0,
    cacheRatio: 0,
    expression: 'tier("base", p * 0 + c * 0 + cr * 0)',
  },
  {
    name: 'overlapping-cache',
    inputRatio: 30,
    cacheRatio: 1,
    createCacheRatio: 1.25,
    expression: 'tier("base", p * 60 + c * 120 + cr * 60 + cc * 75)',
  },
])(
  'merges equal cache prices while preserving explicit free cache for $name',
  async (fixture) => {
    const effective = {
      ModelRatio: fixture.inputRatio,
      CompletionRatio: 2,
      CacheRatio: fixture.cacheRatio,
      CreateCacheRatio: 1.25,
      ImageRatio: 1,
    }
    vi.spyOn(api, 'post').mockImplementation(async (url) => ({
      data: {
        success: true,
        data: url.endsWith('/convert')
          ? {
              effective,
              expression: fixture.expression,
              cache_write_mode:
                fixture.createCacheRatio === undefined ? 'none' : 'standard',
            }
          : {
              effective,
              cache_write_mode:
                fixture.createCacheRatio === undefined ? 'none' : 'standard',
            },
      },
    }))
    const editor = renderEditor({
      name: fixture.name,
      ratio: String(fixture.inputRatio),
      cacheRatio: String(fixture.cacheRatio),
      createCacheRatio: fixture.createCacheRatio?.toString(),
    })
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Convert to expression' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Preview pricing conversion',
    })
    for (const side of ['Before conversion', 'After conversion']) {
      const panel = within(dialog).getByRole('region', { name: side })
      if (fixture.cacheRatio === 1) {
        expect(
          within(panel).queryByText('Cache read price')
        ).not.toBeInTheDocument()
      } else {
        expect(
          within(panel).getByText('Cache read price').nextElementSibling
        ).toHaveTextContent('$0 / 1M token')
      }
      expect(
        within(panel).getByText('Input price').nextElementSibling
      ).toHaveTextContent(`$${fixture.inputRatio * 2} / 1M token`)
    }
    await userEvent
      .setup()
      .click(within(dialog).getByRole('button', { name: 'Apply to draft' }))
    expect(await commit(editor.ref)).toMatchObject({
      billingExpr: fixture.expression,
      cacheRatio: String(fixture.cacheRatio),
    })
  }
)

it('discards a conversion preview when the model being edited changes', async () => {
  const effective = { ModelPrice: 0 }
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: { expression: 'tier("request", fixed(0))', effective },
    },
  })
  const editor = renderEditor({ billingMode: 'per-request', price: '0' })
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Convert to expression' }))
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Preview pricing conversion',
  })
  expect(within(dialog).getAllByText(/^\$0 \/ request$/)).toHaveLength(2)
  editor.reload({ name: 'another-model', ratio: '3' })
  await waitFor(() =>
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  )
  expect(await commit(editor.ref)).toMatchObject({
    name: 'another-model',
    billingMode: 'per-token',
    ratio: '3',
  })
  expect((await commit(editor.ref))?.billingExpr).toBeUndefined()
})

it('previews and retains image quantity and nested quality rules when applying a conversion', async () => {
  const condition =
    'param("quality") == "hd" && (param("size") == "1024x1792" || param("size") == "1792x1024")'
  const expression = `tier("image", fixed(0.04)) * image_count * (${condition} ? 3 : 1)`
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        expression,
        effective: { ModelPrice: 0.04 },
        billing_details: {
          image_count: true,
          request_rules: [{ condition, multiplier: 3 }],
        },
      },
    },
  })
  const editor = renderEditor({
    name: 'dall-e-3',
    billingMode: 'per-request',
    price: '0.04',
  })
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Convert to expression' }))
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Preview pricing conversion',
  })
  for (const name of ['Before conversion', 'After conversion']) {
    const panel = within(dialog).getByRole('region', { name })
    expect(
      within(panel).getByText('Price per image').nextElementSibling
    ).toHaveTextContent('$0.04 / image')
    expect(
      within(panel).getByText('Image count').nextElementSibling
    ).toHaveTextContent('Reserve requested images; settle returned images.')
    expect(within(panel).getByText('× 3')).toBeInTheDocument()
  }
  expect((await commit(editor.ref))?.billingMode).toBe('per-request')
  await userEvent
    .setup()
    .click(within(dialog).getByRole('button', { name: 'Apply to draft' }))
  const committed = await commit(editor.ref)
  expect(committed?.billingExpr).toContain('image_count')
  expect(
    evaluateBillingExpression(committed?.billingExpr || '', {
      imageCount: 2,
      request: { body: { quality: 'hd', size: '1024x1792' } },
    })
  ).toMatchObject({ status: 'success', cost: 240000 })
})

it('shows Gemini audio pricing when the legacy text price is zero without creating a ratio override', async () => {
  const effective = {
    ModelRatio: 0,
    CompletionRatio: 2,
    CacheRatio: 1,
    ImageRatio: 1,
  }
  const expression = 'tier("base", p * 0 + c * 0 + ai * 1)'
  vi.spyOn(api, 'post').mockImplementation(async (url) => ({
    data: {
      success: true,
      data: url.endsWith('/convert')
        ? { expression, effective, billing_details: { audio_input_price: 1 } }
        : { effective, billing_details: { audio_input_price: 1 } },
    },
  }))
  const editor = renderEditor({ name: 'gemini-2.5-flash', ratio: '0' })
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Convert to expression' }))
  const dialog = await screen.findByRole('alertdialog', {
    name: 'Preview pricing conversion',
  })
  for (const name of ['Before conversion', 'After conversion']) {
    const panel = within(dialog).getByRole('region', { name })
    expect(
      within(panel).getByText('Audio input price').nextElementSibling
    ).toHaveTextContent('$1 / 1M token')
  }
  await userEvent
    .setup()
    .click(within(dialog).getByRole('button', { name: 'Apply to draft' }))
  const committed = await commit(editor.ref)
  expect(committed?.billingExpr).toBe(expression)
  expect(committed?.audioRatio).toBe('')
})

it.each([0, 2])(
  'keeps an independent image ratio of %s visible in both conversion panels',
  async (imageRatio) => {
    const effective = {
      ModelRatio: 1,
      CompletionRatio: 2,
      ImageRatio: imageRatio,
    }
    vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          expression: `tier("base", p * 2 + c * 4 + img * ${imageRatio * 2})`,
          effective,
        },
      },
    })
    renderEditor({ imageRatio: String(imageRatio) })
    await userEvent
      .setup()
      .click(screen.getByRole('button', { name: 'Convert to expression' }))
    const dialog = await screen.findByRole('alertdialog', {
      name: 'Preview pricing conversion',
    })
    for (const title of ['Before conversion', 'After conversion']) {
      const panel = within(dialog).getByRole('region', { name: title })
      expect(within(panel).getByText('Image input price')).toBeVisible()
      expect(
        within(panel).getByText('Image input price').nextElementSibling
      ).toHaveTextContent(`$${imageRatio * 2} / 1M token`)
    }
  }
)

it('opens expression pricing by default when an existing model has no configured prices', () => {
  const editor = renderEditor({
    ratio: '',
    completionRatio: '',
    billingMode: 'per-token',
  })
  expect(screen.getByRole('tab', { name: 'Expression' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  expect(editor.dirty).toHaveBeenLastCalledWith(false)
})

it('preserves a legacy per-request draft when conversion is unsupported', async () => {
  vi.spyOn(api, 'post').mockResolvedValue({
    data: {
      success: true,
      data: {
        unsupported_reason:
          'Image prices with count, size or quality adjustments must be converted manually.',
      },
    },
  })
  const editor = renderEditor({
    name: 'gpt-image-2',
    billingMode: 'per-request',
    price: '0',
  })
  await userEvent
    .setup()
    .click(screen.getByRole('button', { name: 'Convert to expression' }))
  expect(await screen.findByRole('status')).toHaveTextContent(
    'Image prices with count, size or quality adjustments must be converted manually.'
  )
  const draft = await commit(editor.ref)
  expect(draft).toMatchObject({ billingMode: 'per-request', price: '0' })
  expect(draft?.billingExpr).toBeUndefined()
})

it('defaults to USD, remembers a currency choice and restores it when reopened', async () => {
  const editor = renderEditor()
  expect(
    screen.getByRole('combobox', { name: 'Pricing currency' })
  ).toHaveTextContent('US dollar (USD)')
  expect(screen.getByRole('textbox', { name: 'Input price' })).toHaveValue('2')
  await selectCurrency('Site currency (CNY)')
  expect(screen.getByRole('textbox', { name: 'Input price' })).toHaveValue('14')
  editor.unmount()
  // Rehydrate from browser storage, rather than relying on the live store.
  const stored = localStorage.getItem('model-pricing-preferences') ?? ''
  expect(stored).not.toBe('')
  usePricingPreferencesStore.setState({ currency: 'USD' })
  localStorage.setItem('model-pricing-preferences', stored)
  await usePricingPreferencesStore.persist.rehydrate()
  renderEditor()
  expect(
    screen.getByRole('combobox', { name: 'Pricing currency' })
  ).toHaveTextContent('Site currency (CNY)')
})

it('opens currency help by keyboard and restores focus after Escape', async () => {
  renderEditor()
  const user = userEvent.setup()
  const help = screen.getByRole('button', { name: 'About pricing currency' })
  help.focus()
  await user.keyboard('{Enter}')
  const dialog = await screen.findByRole('dialog', {
    name: 'About pricing currency',
  })
  expect(
    within(dialog).getByText(/The system always bills in USD/)
  ).toBeVisible()
  expect(
    within(dialog).getByText('Current exchange rate: 1 USD = 7 CNY')
  ).toBeVisible()
  await user.keyboard('{Escape}')
  await waitFor(() => expect(help).toHaveFocus())
})

it('switches currencies and refreshes exchange rates without changing stored prices or dirty state', async () => {
  const editor = renderEditor({ ratio: '0.123456789123', cacheRatio: '0' })
  const before = await commit(editor.ref)
  editor.dirty.mockClear()
  await selectCurrency('Site currency (CNY)')
  await selectCurrency('US dollar (USD)')
  await selectCurrency('Site currency (CNY)')
  act(() =>
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 3,
      },
    })
  )
  expect(await commit(editor.ref)).toEqual(before)
  expect(editor.dirty).not.toHaveBeenCalledWith(true)
  expect(screen.getByRole('textbox', { name: 'Cache read price' })).toHaveValue(
    '0'
  )
})

it('converts edited token prices to USD ratios while preserving unfinished decimals and disabled lanes', async () => {
  const editor = renderEditor()
  await selectCurrency('Site currency (CNY)')
  const input = screen.getByRole('textbox', { name: 'Input price' })
  fireEvent.change(input, { target: { value: '21.' } })
  expect(input).toHaveValue('21.')
  fireEvent.change(screen.getByRole('textbox', { name: 'Completion price' }), {
    target: { value: '42' },
  })
  expect(await commit(editor.ref)).toMatchObject({
    ratio: '1.5',
    completionRatio: '2',
    cacheRatio: '',
  })
  await selectCurrency('US dollar (USD)')
  expect(input).toHaveValue('3')
  expect(await commit(editor.ref)).toMatchObject({
    ratio: '1.5',
    completionRatio: '2',
  })
})

it.each(['0', '0.0000007', '14'])(
  'saves the CNY per-request price %s as USD without display rounding',
  async (price) => {
    const editor = renderEditor({ billingMode: 'per-request', price: '1' })
    await selectCurrency('Site currency (CNY)')
    const fixed = screen.getByRole('textbox', { name: 'Fixed price' })
    fireEvent.change(fixed, { target: { value: price } })
    const expected = { '0': '0', '0.0000007': '0.0000001', '14': '2' }[price]
    expect(Number((await commit(editor.ref))?.price)).toBe(Number(expected))
    await selectCurrency('US dollar (USD)')
    expect(fixed).toHaveValue(expected)
  }
)

it('uses the custom currency exchange rate instead of the CNY exchange rate', async () => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CUSTOM',
      usdExchangeRate: 7,
      customCurrencySymbol: '€',
      customCurrencyExchangeRate: 0.5,
    },
  })
  const editor = renderEditor({ price: '2', billingMode: 'per-request' })
  await selectCurrency('Site currency (€)')
  const fixed = screen.getByRole('textbox', { name: 'Fixed price' })
  expect(fixed).toHaveValue('1')
  fireEvent.change(fixed, { target: { value: '7' } })
  expect(await commit(editor.ref)).toMatchObject({ price: '14' })
})

it.each([0, -1, Infinity, Number.NaN, undefined])(
  'disables site currency and falls back to USD for invalid rate %s',
  async (rate) => {
    usePricingPreferencesStore.setState({ currency: 'site' })
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: rate as number,
      },
    })
    renderEditor()
    expect(screen.getByRole('textbox', { name: 'Input price' })).toHaveValue(
      '2'
    )
    expect(
      screen.getByText(
        'The site exchange rate is invalid. Prices are shown in USD.'
      )
    ).toBeVisible()
    await userEvent.click(
      screen.getByRole('combobox', { name: 'Pricing currency' })
    )
    expect(
      await screen.findByRole('option', { name: 'Site currency (CNY)' })
    ).toHaveAttribute('aria-disabled', 'true')
  }
)

it.each(['USD', 'TOKENS'] as const)(
  'offers only USD when the site uses %s',
  async (type) => {
    usePricingPreferencesStore.setState({ currency: 'site' })
    useSystemConfigStore.getState().setConfig({
      currency: { ...DEFAULT_CURRENCY_CONFIG, quotaDisplayType: type },
    })
    renderEditor()
    await userEvent.click(
      screen.getByRole('combobox', { name: 'Pricing currency' })
    )
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(
      screen.getByRole('option', { name: 'US dollar (USD)' })
    ).toBeVisible()
  }
)

it('blocks a non-finite conversion and allows saving after the amount is corrected', async () => {
  useSystemConfigStore.getState().setConfig({
    currency: {
      ...DEFAULT_CURRENCY_CONFIG,
      quotaDisplayType: 'CNY',
      usdExchangeRate: 1e-308,
    },
  })
  const editor = renderEditor({ price: '1', billingMode: 'per-request' })
  await selectCurrency('Site currency (CNY)')
  const fixed = screen.getByRole('textbox', { name: 'Fixed price' })
  fireEvent.change(fixed, { target: { value: '14' } })
  expect(fixed).toHaveAttribute('aria-invalid', 'true')
  expect(await commit(editor.ref)).toBeNull()
  expect(
    screen.getByText(
      'The converted price must be a finite, non-negative number.'
    )
  ).toBeVisible()
  fireEvent.change(fixed, { target: { value: '0' } })
  expect(await commit(editor.ref)).toMatchObject({ price: '0' })
})

it('converts tier price coefficients but leaves token thresholds and rule multipliers unchanged', async () => {
  const expr =
    'len <= 200000 ? tier("short", p * 2 + c * 4) : tier("long", p * 4 + c * 8)'
  const editor = renderEditor({
    billingMode: 'tiered_expr',
    billingExpr: expr,
    requestRuleExpr: '(header("x-priority") == "high" ? 2 : 1)',
  })
  const before = await commit(editor.ref)
  await selectCurrency('Site currency (CNY)')
  expect(await commit(editor.ref)).toEqual(before)
  const inputs = screen.getAllByRole('textbox', { name: 'Input price' })
  expect(inputs[0]).toHaveValue('14')
  fireEvent.change(inputs[0], { target: { value: '21' } })
  const saved = await commit(editor.ref)
  const config = tryParseVisualConfig(saved?.billingExpr ?? '')
  expect(config?.tiers[0].input_unit_cost).toBe(3)
  expect(config?.tiers[0].conditions).toEqual([
    { var: 'len', op: '<=', value: 200000 },
  ])
  expect(saved?.requestRuleExpr).toBe(before?.requestRuleExpr)
})

it('keeps custom raw expressions byte-for-byte intact on currency changes', async () => {
  const expr = 'tier("custom", max(p * 2, 100))'
  const editor = renderEditor({
    billingMode: 'tiered_expr',
    billingExpr: expr,
  })
  await selectCurrency('Site currency (CNY)')
  expect(await commit(editor.ref)).toMatchObject({ billingExpr: expr })
  expect(
    screen.getByText(
      'Raw expressions and presets use USD. Currency selection only converts visual price inputs and monetary previews.'
    )
  ).toBeVisible()
})

it('keeps time pricing and request rules unchanged when opening the simulator', async () => {
  const user = userEvent.setup()
  const expression =
    'hour("Asia/Shanghai") < 12 ? tier("peak", p * 3 + c * 9) : tier("off_peak", p * 1.5 + c * 4.5)'
  const rules = '(header("tier") == "fast" ? 2 : 1)'
  const editor = renderEditor({
    billingMode: 'tiered_expr',
    billingExpr: expression,
    requestRuleExpr: rules,
  })
  const before = await commit(editor.ref)
  await user.click(screen.getByRole('button', { name: 'Request simulation' }))
  fireEvent.input(
    screen.getByRole('textbox', { name: 'Simulated request body' }),
    { target: { value: '{"unused":1}' } }
  )
  await selectCurrency('Site currency (CNY)')
  expect(await commit(editor.ref)).toEqual(before)
  expect(before?.billingExpr).toBe(expression)
  expect(before?.requestRuleExpr).toBe(rules)
})

it('converts task base charges and second, token and credit prices, including whole-column fill', async () => {
  const schema: BillingUsageSchema = {
    seconds: { type: 'number', unit: 'second' },
    tokens: { type: 'number', unit: 'token' },
    credits: { type: 'number', unit: 'credit' },
    mode: { enum: ['std', 'pro'] },
  }
  const editor = renderEditor(
    {
      billingMode: 'tiered_expr',
      billingExpr: 'tier("base", u("seconds") * 1)',
    },
    schema
  )
  await selectCurrency('Site currency (CNY)')
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Additional charge: mode: std' }),
    {
      target: { value: '7' },
    }
  )
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Unit price: tokens: mode: std' }),
    {
      target: { value: '70' },
    }
  )
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Unit price: credits: mode: std' }),
    {
      target: { value: '0.7' },
    }
  )
  const secondsHeader = screen.getByRole('columnheader', { name: /seconds/ })
  await userEvent.click(
    within(secondsHeader).getByRole('button', { name: 'Fill entire column' })
  )
  fireEvent.change(
    screen.getByRole('textbox', { name: 'Fill entire column' }),
    { target: { value: '14' } }
  )
  await userEvent.click(
    screen.getByRole('button', { name: 'Apply to all rows' })
  )
  expect(
    screen.getByRole('textbox', { name: 'Unit price: seconds: mode: std' })
  ).toHaveValue('14')
  expect(
    screen.getByRole('textbox', { name: 'Unit price: seconds: mode: pro' })
  ).toHaveValue('14')
  const saved = await commit(editor.ref)
  const config = tryParseTaskVisualConfig(saved?.billingExpr ?? '', schema)
  expect(config?.tiers[0]).toMatchObject({
    constant: 1,
    unitPrices: { seconds: 2, tokens: 10, credits: 0.1 },
  })
  expect(config?.tiers[1].unitPrices.seconds).toBe(2)
  expect(saved?.billingExpr).toContain('/ 1000000')
  await selectCurrency('US dollar (USD)')
  expect(await commit(editor.ref)).toEqual(saved)
})

it('converts task unit prices without enum tiers and updates the monetary preview', async () => {
  const schema: BillingUsageSchema = {
    seconds: { type: 'number', unit: 'second' },
  }
  const editor = renderEditor(
    {
      billingMode: 'tiered_expr',
      billingExpr: 'tier("base", u("seconds") * 1)',
    },
    schema
  )
  await selectCurrency('Site currency (CNY)')
  fireEvent.change(screen.getByRole('textbox', { name: 'seconds' }), {
    target: { value: '14' },
  })
  fireEvent.change(screen.getByRole('textbox', { name: 'Additional charge' }), {
    target: { value: '7' },
  })
  expect(await commit(editor.ref)).toMatchObject({
    billingExpr: 'tier("base", 1 + u("seconds") * 2)',
  })
  expect(screen.getByText(/= ¥77$/)).toBeVisible()
})

it('switches currency using the keyboard without changing the saved configuration', async () => {
  const editor = renderEditor()
  const original = await commit(editor.ref)
  const user = userEvent.setup()
  screen.getByRole('combobox', { name: 'Pricing currency' }).focus()
  await user.keyboard('{ArrowDown}{End}{Enter}')
  expect(
    screen.getByRole('combobox', { name: 'Pricing currency' })
  ).toHaveTextContent('Site currency (CNY)')
  expect(await commit(editor.ref)).toEqual(original)
})

it('shows the estimated token cost in the selected currency while token quantities stay unchanged', async () => {
  renderEditor({
    billingMode: 'tiered_expr',
    billingExpr: 'tier("base", p * 2 + c * 4)',
  })
  const tokens = screen.getByRole('spinbutton', { name: 'Input tokens' })
  fireEvent.change(tokens, { target: { value: '1000000' } })
  expect(screen.getByText('Estimated cost: $2')).toBeVisible()
  await selectCurrency('Site currency (CNY)')
  expect(tokens).toHaveValue(1000000)
  expect(screen.getByText('Estimated cost: ¥14')).toBeVisible()
})

it('keeps an empty per-request amount empty when currencies change', async () => {
  const editor = renderEditor({
    billingMode: 'per-request',
    price: '1',
    ratio: '',
    completionRatio: '',
  })
  const fixed = screen.getByRole('textbox', { name: 'Fixed price' })
  fireEvent.change(fixed, { target: { value: '' } })
  await selectCurrency('Site currency (CNY)')
  expect(fixed).toHaveValue('')
  expect(await commit(editor.ref)).toMatchObject({ price: '' })
})

it('does not block saving valid prices when a preview quantity has a fractional step', async () => {
  const schema: BillingUsageSchema = {
    seconds: { type: 'number', unit: 'second' },
  }
  const editor = renderEditor(
    {
      billingMode: 'tiered_expr',
      billingExpr: 'tier("base", u("seconds") * 1)',
    },
    schema
  )
  fireEvent.change(screen.getByRole('spinbutton'), {
    target: { value: '0.5' },
  })
  expect(await commit(editor.ref)).toMatchObject({
    billingExpr: 'tier("base", u("seconds") * 1)',
  })
})

it('clears an invalid amount draft when pricing is reloaded with the same saved value', async () => {
  const saved: Partial<ModelRatioData> = {
    billingMode: 'per-request',
    price: '1',
    ratio: '',
    completionRatio: '',
  }
  const editor = renderEditor(saved)
  await selectCurrency('Site currency (CNY)')
  fireEvent.change(screen.getByRole('textbox', { name: 'Fixed price' }), {
    target: { value: '9'.repeat(309) },
  })
  expect(await commit(editor.ref)).toBeNull()
  editor.reload({ ...saved })
  expect(screen.getByRole('textbox', { name: 'Fixed price' })).toHaveValue('7')
  expect(await commit(editor.ref)).toMatchObject({ price: '1' })
})

it('preserves time billing on direct save and blocks incomplete local conditions', async () => {
  const expression =
    'hour("Asia/Shanghai") >= 9 ? tier("peak", p * 3 + cr * 0 + c * 9) : tier("off", p * 1 + c * 2)'
  const requestRuleExpr = '(header("x-plan") == "fast" ? 2.00 : 1)'
  const editor = renderEditor({
    billingMode: 'tiered_expr',
    billingExpr: expression,
    requestRuleExpr,
  })
  expect(await commit(editor.ref)).toMatchObject({
    billingExpr: expression,
    requestRuleExpr,
  })
  await selectCurrency('Site currency (CNY)')
  expect(await commit(editor.ref)).toMatchObject({
    billingExpr: expression,
    requestRuleExpr,
  })
  fireEvent.change(screen.getByRole('textbox', { name: 'Condition value' }), {
    target: { value: '' },
  })
  expect(await commit(editor.ref)).toBeNull()
  fireEvent.change(screen.getByRole('textbox', { name: 'Condition value' }), {
    target: { value: '10' },
  })
  expect(await commit(editor.ref)).toMatchObject({
    billingExpr: expression.replace('>= 9', '>= 10'),
    requestRuleExpr,
  })
})
