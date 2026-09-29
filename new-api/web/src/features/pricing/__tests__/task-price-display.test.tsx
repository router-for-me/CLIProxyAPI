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
import { act, render, screen, cleanup, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { DynamicPricingBreakdown } from '../components/dynamic-pricing-breakdown'
import { ModelCard } from '../components/model-card'
import { ModelDetailsContent } from '../components/model-details'
import { ModelPriceCell } from '../components/model-price-cell'
import { getTaskPricingDisplayTiers } from '../lib/task-matrix-display'
import {
  hasSimpleTaskPricing,
  taskPriceLabel,
  taskEnumLabel,
  taskPricingConditions,
  taskUsageUnitLabel,
} from '../lib/task-price-display'
import type { PricingModel, BillingUsageSchema } from '../types'

vi.mock('@visactor/react-vchart', () => ({ VChart: () => null }))

it('shows an explicit free request price alongside token prices with distinct units', () => {
  render(
    <DynamicPricingBreakdown
      billingExpr='len < 1000 ? tier("free", fixed(0)) : tier("tokens", p * 2 + c * 8)'
      matchedTierLabel='free'
    />
  )
  expect(screen.getAllByText('$0/request').length).toBeGreaterThan(0)
  expect(screen.getAllByText('Input / 1M token').length).toBeGreaterThan(0)
  expect(screen.getAllByText('Price per request').length).toBeGreaterThan(0)
})

it('renders weekday and hour conditions as time windows instead of expression source', () => {
  const condition =
    'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18))'
  render(
    <DynamicPricingBreakdown
      billingExpr={`${condition} ? tier("peak", p * 3 + c * 9 + cr * 0.1) : tier("off_peak", p * 1.5 + c * 4.5 + cr * 0.05)`}
    />
  )
  expect(
    screen.getAllByText('Mon–Fri 09:00–12:00 or 14:00–18:00 (Asia/Shanghai)')
      .length
  ).toBeGreaterThan(0)
  expect(
    screen.getAllByText(
      'Outside these times: Mon–Fri 09:00–12:00 or 14:00–18:00 (Asia/Shanghai)'
    ).length
  ).toBeGreaterThan(0)
  expect(screen.queryByText(/weekday\(/)).not.toBeInTheDocument()
})

const model: PricingModel = {
  id: 1,
  model_name: 'incho_music',
  quota_type: 0,
  model_ratio: 1,
  completion_ratio: 1,
  enable_groups: ['default'],
  billing_mode: 'tiered_expr',
  billing_expr: 'tier("music", u("clips") * 0.22)',
  billing_usage_schema: {
    clips: {
      type: 'number',
      unit: 'count',
      description: { en: 'Song generation unit price', zh: '生成歌曲单价' },
    },
    action: {
      enum: ['music'],
      enumLabels: { music: { en: 'Generate songs', zh: '生成歌曲' } },
      description: { en: 'Generate songs', zh: '生成歌曲' },
    },
  },
}
const clients: QueryClient[] = []

it('shows nested task conditions and prices in detail and group tables without ambiguous log matches', () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { data: { groups: [] } } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const nestedModel: PricingModel = {
    ...model,
    billing_expr:
      'tier("standard", u("seconds") * (hour("Asia/Shanghai") >= 18 && hour("Asia/Shanghai") < 22 ? (u("resolution") == "4K" ? 0.12 : 0.072) : (u("resolution") == "4K" ? 0.15 : 0.09)))',
    billing_usage_schema: {
      seconds: { type: 'number', unit: 'second' },
      resolution: { enum: ['768P', '4K'] },
    },
  }
  render(
    <QueryClientProvider client={client}>
      <ModelCard model={nestedModel} onClick={vi.fn()} />
      <ModelPriceCell model={nestedModel} />
      <ModelDetailsContent
        model={nestedModel}
        groupRatio={{ default: 2 }}
        usableGroup={{ default: { desc: '', ratio: 2 } }}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
      <DynamicPricingBreakdown
        billingExpr={nestedModel.billing_expr}
        usageSchema={nestedModel.billing_usage_schema}
        matchedTierLabel='standard'
        usageFacts={{ seconds: 10, resolution: '4K' }}
      />
    </QueryClientProvider>
  )
  expect(
    screen.queryByText('Special billing expression')
  ).not.toBeInTheDocument()
  expect(
    screen.getAllByText(/resolution: 4K · 18:00–22:00 \(Asia\/Shanghai\)/)
      .length
  ).toBeGreaterThan(0)
  expect(
    screen.getAllByText(/resolution: 768P · Outside these times:/).length
  ).toBeGreaterThan(0)
  expect(screen.getByText('$0.072 – $0.15')).toBeVisible()
  expect(screen.getAllByText('$0.24').length).toBeGreaterThan(0)
  expect(screen.queryByText('Matched')).not.toBeInTheDocument()
})

it('explains missing task metadata while retaining the original expression', () => {
  const expression = 'tier("base", u("seconds") * 0.09)'
  render(<DynamicPricingBreakdown billingExpr={expression} />)
  expect(
    screen.getByText(
      'Task usage metadata is unavailable. Pricing details cannot be displayed.'
    )
  ).toBeVisible()
  expect(screen.getByText(expression)).toBeVisible()
})

it('falls back for omitted count labels and preserves canonical units for other quantities', () => {
  expect(taskUsageUnitLabel({ unit: 'count' }, 'zhCN', '次')).toBe('次')
  expect(
    taskUsageUnitLabel(
      { unit: 'token', unitLabel: { en: 'image' } },
      'en',
      '1M token'
    )
  ).toBe('1M token')
})

const imageModel: PricingModel = {
  ...model,
  model_name: 'image-model',
  billing_expr: 'tier("images", u("image_count") * 0.2)',
  billing_usage_schema: {
    image_count: {
      type: 'number',
      unit: 'count',
      unitLabel: { en: 'image', zh: '张', 'zh-TW': '張' },
      description: { en: 'Image generation unit price', zh: '图片生成单价' },
    },
  },
}

it.each([false, true])(
  'shows localized image labels and units in base and group pricing when configured=%s',
  async (configured) => {
    vi.spyOn(api, 'get').mockResolvedValue({ data: { data: { groups: [] } } })
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    clients.push(client)
    render(
      <QueryClientProvider client={client}>
        <ModelDetailsContent
          model={{
            ...imageModel,
            billing_expr: configured ? imageModel.billing_expr : undefined,
          }}
          groupRatio={{ default: 2 }}
          usableGroup={{ default: { desc: '', ratio: 2 } }}
          endpointMap={{}}
          autoGroups={[]}
          priceRate={1}
          usdExchangeRate={1}
          tokenUnit='M'
        />
      </QueryClientProvider>
    )
    await act(() => i18next.changeLanguage('zhCN'))
    expect(screen.getAllByText('图片生成单价')).toHaveLength(2)
    expect(screen.getAllByText(configured ? '/ 张' : '张')).toHaveLength(2)
    expect(screen.queryByText('image_count')).not.toBeInTheDocument()
    if (configured) {
      expect(screen.getByText('$0.2')).toBeVisible()
      expect(screen.getByText('$0.4')).toBeVisible()
    }
  }
)

it('updates count unit labels across cards, table cells and breakdowns with locale fallback', async () => {
  render(
    <>
      <div data-testid='card'>
        <ModelCard model={imageModel} onClick={() => {}} />
      </div>
      <div data-testid='cell'>
        <ModelPriceCell model={imageModel} />
      </div>
      <div data-testid='breakdown'>
        <DynamicPricingBreakdown
          billingExpr={imageModel.billing_expr}
          usageSchema={imageModel.billing_usage_schema}
        />
      </div>
    </>
  )
  for (const [language, unit] of [
    ['en', 'image'],
    ['zhTW', '張'],
    ['zhCN', '张'],
    ['fr', 'image'],
  ]) {
    await act(() => i18next.changeLanguage(language))
    for (const surface of ['card', 'cell', 'breakdown']) {
      expect(screen.getByTestId(surface)).toHaveTextContent(
        new RegExp(`/\\s*${unit}`)
      )
    }
  }
})

afterEach(async () => {
  cleanup()
  clients.forEach((client) => client.clear())
  clients.length = 0
  vi.restoreAllMocks()
  await i18next.changeLanguage('en')
})

it('refreshes memoized provider prices when the group or display currency changes', () => {
  const previous = useSystemConfigStore.getState().config.currency
  useSystemConfigStore
    .getState()
    .setConfig({ currency: DEFAULT_CURRENCY_CONFIG })
  try {
    const shared = {
      ...model,
      enable_groups: ['default', 'premium'],
      group_ratio: { default: 1, premium: 3 },
      billing_plugin_variants: [
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          billing_expr: model.billing_expr ?? '',
          billing_usage_schema: model.billing_usage_schema ?? {},
        },
      ],
    }
    const view = render(
      <ModelPriceCell model={shared} options={{ selectedGroup: 'default' }} />
    )
    expect(view.container).toHaveTextContent('0.22/unit')
    view.rerender(
      <ModelPriceCell model={shared} options={{ selectedGroup: 'premium' }} />
    )
    expect(view.container).toHaveTextContent('0.66/unit')
    act(() =>
      useSystemConfigStore.getState().setConfig({
        currency: {
          ...DEFAULT_CURRENCY_CONFIG,
          quotaDisplayType: 'CNY',
          usdExchangeRate: 2,
        },
      })
    )
    expect(view.container).toHaveTextContent('1.32/unit')
  } finally {
    act(() => useSystemConfigStore.getState().setConfig({ currency: previous }))
  }
})

it('shows one standard task price and a localized group price without duplicate tiers', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { data: { groups: [] } } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ModelDetailsContent
        model={model}
        groupRatio={{ default: 2 }}
        usableGroup={{ default: { desc: '', ratio: 2 } }}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={7}
        tokenUnit='M'
      />
    </QueryClientProvider>
  )
  expect(
    screen.getAllByText('Song generation unit price', { exact: false })
  ).toHaveLength(2)
  expect(screen.queryByText('Tiered price table')).not.toBeInTheDocument()
  expect(screen.queryByText('Dynamic Pricing')).not.toBeInTheDocument()
  expect(screen.queryByText('music')).not.toBeInTheDocument()
  expect(screen.getByText('$0.22')).toBeVisible()
  expect(screen.getByText('$0.44')).toBeVisible()
  await act(() => i18next.changeLanguage('zhCN'))
  expect(screen.getAllByText('生成歌曲单价', { exact: false })).toHaveLength(2)
  await act(() => i18next.changeLanguage('fr'))
  expect(
    screen.getAllByText('Song generation unit price', { exact: false })
  ).toHaveLength(2)
})

it('labels even a single task price on model cards', async () => {
  render(<ModelCard model={model} onClick={() => {}} />)
  expect(screen.getByText('Song generation unit price')).toBeVisible()
  await act(() => i18next.changeLanguage('zhCN'))
  expect(screen.getByText('生成歌曲单价')).toBeVisible()
})

it('preserves condition tables, boolean states and additional charges', () => {
  render(
    <DynamicPricingBreakdown
      billingExpr='u("audio") == true ? tier("audio", 1 + u("seconds") * 0.8) : tier("silent", u("seconds") * 0.4)'
      usageSchema={{
        seconds: {
          type: 'number',
          unit: 'second',
          description: { en: 'Video generation unit price' },
        },
        audio: {
          type: 'boolean',
          description: { en: 'Whether audio is generated' },
        },
      }}
    />
  )
  expect(
    screen.getByRole('columnheader', { name: 'Applicable conditions' })
  ).toBeVisible()
  expect(screen.queryByText('Pricing conditions')).not.toBeInTheDocument()
  expect(
    screen.getAllByText('Whether audio is generated: Yes').length
  ).toBeGreaterThan(0)
  expect(
    screen.getAllByText('Whether audio is generated: No').length
  ).toBeGreaterThan(0)
  expect(screen.getAllByText('Additional charge').length).toBeGreaterThan(0)
  expect(screen.getAllByText('Video generation unit price')[0]).toHaveClass(
    'whitespace-normal',
    'break-words'
  )
})

it('keeps rules and custom expressions out of the simple price layout', () => {
  expect(hasSimpleTaskPricing(model)).toBe(true)
  expect(
    hasSimpleTaskPricing({
      ...model,
      billing_expr: `${model.billing_expr}|||when(header("x-fast") == "true") * 2`,
    })
  ).toBe(false)
  expect(
    hasSimpleTaskPricing({
      ...model,
      billing_expr: 'max(u("clips"), 2) * 0.22',
    })
  ).toBe(false)
  expect(taskPriceLabel(undefined, 'clips', 'fr')).toBe('clips')
  expect(
    taskPriceLabel({ en: 'Song price', zh: '歌曲单价' }, 'clips', 'zh-TW')
  ).toBe('歌曲单价')
  expect(
    taskPriceLabel({ en: 'Song price', zh: '歌曲单价' }, 'clips', 'ja')
  ).toBe('Song price')
})

const videoSchema: BillingUsageSchema = {
  tokens: {
    type: 'number',
    unit: 'token',
    description: { en: 'Billing token unit price', zh: '计费 Token 单价' },
  },
  resolution: {
    enum: ['480p', '720p', '1080p'],
    description: { en: 'Output resolution', zh: '输出分辨率' },
  },
  video_input: {
    enum: ['none', 'video'],
    description: { en: 'Reference video input', zh: '参考视频输入' },
    enumLabels: {
      none: { en: 'No reference video', zh: '无参考视频' },
      video: { en: 'With reference video', zh: '有参考视频' },
    },
  },
}
const videoExpression =
  'u("video_input") == "none" ? tier("none", u("tokens") * 10 / 1000000) : tier("video", u("tokens") * 6 / 1000000)'

it('uses plugin option labels and infers a unique fallback without expanding unrelated fields', async () => {
  render(
    <DynamicPricingBreakdown
      billingExpr={videoExpression}
      usageSchema={videoSchema}
    />
  )
  expect(screen.getAllByText('No reference video')).toHaveLength(2)
  expect(screen.getAllByText('With reference video')).toHaveLength(2)
  expect(screen.queryByText('Other cases')).not.toBeInTheDocument()
  expect(screen.queryByText('Pricing conditions')).not.toBeInTheDocument()
  expect(screen.queryByText('480p')).not.toBeInTheDocument()
  await act(() => i18next.changeLanguage('zhCN'))
  expect(screen.getAllByText('无参考视频')).toHaveLength(2)
  expect(screen.getAllByText('有参考视频')).toHaveLength(2)
  await act(() => i18next.changeLanguage('ja'))
  expect(screen.getAllByText('With reference video')).toHaveLength(2)
})

it('names ambiguous fallback rows other cases and keeps unmatched enum values readable', () => {
  const schema = {
    ...videoSchema,
    video_input: {
      ...videoSchema.video_input,
      enum: ['none', 'video', 'mixed'],
    },
  }
  render(
    <DynamicPricingBreakdown
      billingExpr={videoExpression}
      usageSchema={schema}
    />
  )
  expect(screen.getAllByText('Other cases')).toHaveLength(2)
  expect(taskEnumLabel(schema.video_input, 'mixed', 'zhCN')).toBe('mixed')
  expect(
    taskPricingConditions(
      [{ field: 'video_input', value: 'mixed' }],
      schema,
      'en',
      (key) => key
    )
  ).toBe('Reference video input: mixed')
  expect(
    taskEnumLabel(
      { enum: ['x'], enumLabels: { x: { en: 'English', zh: '中文' } } },
      'x',
      'fr'
    )
  ).toBe('English')
})

it('preserves all conditions and leaves multiple possible fallback combinations unspecified', () => {
  const tiers = getTaskPricingDisplayTiers(
    'u("resolution") == "720p" && u("video_input") == "none" ? tier("one", u("tokens") * 10 / 1000000) : tier("rest", u("tokens") * 6 / 1000000)',
    videoSchema
  )
  expect(
    taskPricingConditions(tiers[0].conditions, videoSchema, 'en', (key) => key)
  ).toBe('Output resolution: 720p · No reference video')
  expect(tiers[1].conditions).toEqual([])
})

it('uses the same recharge conversion and token unit in task condition prices', () => {
  render(
    <DynamicPricingBreakdown
      billingExpr={videoExpression}
      usageSchema={videoSchema}
      taskPriceOptions={{
        showRechargePrice: true,
        priceRate: 1,
        usdExchangeRate: 2,
      }}
    />
  )
  expect(screen.getAllByText('$5/1M token')).toHaveLength(2)
  expect(screen.getAllByText('$3/1M token')).toHaveLength(2)
})

it('switches provider group prices, localized conditions and examples, and shows unconfigured providers', async () => {
  vi.spyOn(api, 'get').mockResolvedValue({ data: { data: { groups: [] } } })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  const shared: PricingModel = {
    ...model,
    quota_type: 1,
    model_price: 0.25,
    billing_plugin_variants: [
      {
        plugin_key: 'alpha',
        plugin_name: 'Alpha',
        billing_expr: 'tier("alpha", u("seconds") * 0.4)',
        billing_usage_schema: {
          seconds: {
            type: 'number',
            unit: 'second',
            description: 'Video unit price',
          },
        },
        billing_usage_examples: [
          { label: 'Alpha sample', facts: { seconds: 5 } },
        ],
      },
      {
        plugin_key: 'beta',
        plugin_name: 'Beta',
        billing_expr:
          'u("mode") == "pro" ? tier("pro", u("credits") * 3) : tier("base", u("credits") * 1.5)',
        billing_usage_schema: {
          credits: {
            type: 'number',
            unit: 'credit',
            description: 'Credit unit price',
          },
          mode: {
            enum: ['base', 'pro'],
            enumLabels: {
              base: { en: 'Standard mode', zh: '标准模式' },
              pro: { en: 'Professional mode', zh: '专业模式' },
            },
          },
        },
        billing_usage_examples: [
          { label: 'Beta sample', facts: { credits: 2, mode: 'base' } },
        ],
      },
      {
        plugin_key: 'gamma',
        plugin_name: 'Gamma',
        billing_expr: '',
        billing_usage_schema: { images: { type: 'number', unit: 'count' } },
      },
      {
        plugin_key: 'delta',
        plugin_name: 'Delta',
        billing_mode: 'ratio',
        billing_expr: '',
        billing_usage_schema: { images: { type: 'number', unit: 'count' } },
      },
    ],
  }
  render(
    <QueryClientProvider client={client}>
      <ModelDetailsContent
        model={shared}
        groupRatio={{ default: 2 }}
        usableGroup={{ default: { desc: '', ratio: 2 } }}
        endpointMap={{}}
        autoGroups={[]}
        priceRate={1}
        usdExchangeRate={1}
        tokenUnit='M'
      />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  const alpha = screen.getByRole('tab', { name: 'Alpha' })
  expect(alpha).toHaveAttribute('aria-selected', 'true')
  let panel = screen.getByRole('tabpanel', { name: 'Alpha' })
  expect(within(panel).getByText('Alpha sample')).toBeVisible()
  expect(within(panel).getByText('$0.8')).toBeVisible()
  await user.click(alpha)
  await user.keyboard('{ArrowRight}')
  expect(screen.getByRole('tab', { name: 'Beta' })).toHaveFocus()
  await user.keyboard('{Enter}')
  expect(screen.getByRole('tab', { name: 'Beta' })).toHaveAttribute(
    'aria-selected',
    'true'
  )
  panel = screen.getByRole('tabpanel', { name: 'Beta' })
  expect(within(panel).getByText('Beta sample')).toBeVisible()
  expect(within(panel).queryByText('Alpha sample')).not.toBeInTheDocument()
  expect(within(panel).getByText('Professional mode')).toBeVisible()
  expect(within(panel).getByText('$3')).toBeVisible()
  expect(within(panel).getByText('$6')).toBeVisible()
  await act(() => i18next.changeLanguage('zhCN'))
  expect(within(panel).getByText('专业模式')).toBeVisible()
  await act(() => i18next.changeLanguage('en'))
  await user.click(screen.getByRole('tab', { name: 'Gamma' }))
  panel = screen.getByRole('tabpanel', { name: 'Gamma' })
  expect(
    within(panel).getByText(
      'This model is billed by usage, but the administrator has not configured its pricing yet.'
    )
  ).toBeVisible()
  expect(within(panel).queryByRole('table')).not.toBeInTheDocument()
  await user.click(screen.getByRole('tab', { name: 'Delta' }))
  panel = screen.getByRole('tabpanel', { name: 'Delta' })
  expect(within(panel).getByText('$0.5')).toBeVisible()
})

it('shows provider count, price range and missing-price status in both list and card views', () => {
  const shared: PricingModel = {
    ...model,
    billing_mode: undefined,
    billing_expr: undefined,
    billing_plugin_variants: [
      {
        plugin_key: 'alpha',
        plugin_name: 'Alpha',
        billing_expr: 'tier("alpha", u("seconds") * 0.4)',
        billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
      },
      {
        plugin_key: 'beta',
        plugin_name: 'Beta',
        billing_expr: 'tier("beta", u("seconds") * 0.8)',
        billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
      },
      {
        plugin_key: 'gamma',
        plugin_name: 'Gamma',
        billing_expr: '',
        billing_usage_schema: { credits: { type: 'number', unit: 'credit' } },
      },
    ],
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  clients.push(client)
  render(
    <QueryClientProvider client={client}>
      <ModelPriceCell model={shared} />
      <ModelCard model={shared} onClick={vi.fn()} />
    </QueryClientProvider>
  )
  expect(screen.getAllByText(/3 providers/)).toHaveLength(2)
  expect(screen.getAllByText(/Not configured for some providers/)).toHaveLength(
    2
  )
  expect(screen.getByText('0.4 – 0.8/s')).toBeVisible()
  expect(screen.getByText('$0.4 – $0.8')).toBeVisible()
})
