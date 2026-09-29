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
  cleanup,
  render,
  screen,
  within,
  waitFor,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  pricingOptions,
  applyPriceSyncSelections,
  pricingValuesByModel,
} from '@/features/model-pricing/pricing'
import { api } from '@/lib/api'

import { UpstreamRatioSync } from '../models/upstream-ratio-sync'
import {
  getSyncPriceLines,
  type PricingSourceSelections,
} from '../models/upstream-ratio-sync-helpers'
import { UpstreamRatioSyncTable } from '../models/upstream-ratio-sync-table'
import type { PricingSyncModels } from '../types'

const expression = 'tier("base", p * 2 + c * 8 + cr * 0)'
const differences = {
  m: {
    model_ratio: {
      current: 0.5,
      upstreams: { upstream: 1 },
      confidence: { upstream: true },
    },
    completion_ratio: {
      current: 2,
      upstreams: { upstream: 4 },
      confidence: { upstream: true },
    },
  },
}
const prices: PricingSyncModels = {
  m: {
    current: { model_ratio: 0.5, completion_ratio: 2, billing_mode: 'ratio' },
    upstreams: {
      upstream: {
        model_ratio: 1,
        completion_ratio: 4,
        cache_ratio: 0,
        billing_mode: 'ratio',
      },
    },
  },
}
function TableFixture(props: { prices: PricingSyncModels }) {
  const [selectedSources, setSelectedSources] =
    useState<PricingSourceSelections>({})
  return (
    <UpstreamRatioSyncTable
      prices={props.prices}
      differences={differences}
      selectedSources={selectedSources}
      isDisabled={false}
      isSyncing={false}
      onSelectPrices={(selections) =>
        setSelectedSources((previous) => ({
          ...previous,
          ...Object.fromEntries(
            selections.map((selection) => [selection.model, selection.source])
          ),
        }))
      }
      onUnselectPrices={(models) =>
        setSelectedSources((previous) =>
          Object.fromEntries(
            Object.entries(previous).filter(
              ([model]) => !models.includes(model)
            )
          )
        )
      }
    />
  )
}
afterEach(cleanup)

describe('pricing synchronization', () => {
  it('shows every dollar price inline, including explicit zero cache and audio prices', () => {
    render(<TableFixture prices={prices} />)
    expect(screen.queryAllByText('Model ratio')).toHaveLength(0)
    expect(screen.getAllByText('$2')).toHaveLength(2)
    expect(screen.getByText('$8')).toBeVisible()
    expect(screen.getByText('$0')).toBeVisible()
    expect(screen.getByText('Cache Read')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: /details/i })
    ).not.toBeInTheDocument()
    expect(
      getSyncPriceLines(
        {
          model_ratio: 2,
          audio_ratio: 3,
          audio_completion_ratio: 4,
          image_ratio: 0,
        },
        (key) => key
      )
    ).toEqual([
      { label: 'Input', value: '$4' },
      { label: 'Image input', value: '$0' },
      { label: 'Audio input', value: '$12' },
      { label: 'Audio output', value: '$48' },
    ])
  })

  it('parses expression prices inline while copying the unchanged source expression', async () => {
    render(
      <TableFixture
        prices={{
          m: {
            current: {},
            upstreams: {
              upstream: {
                billing_mode: 'tiered_expr',
                billing_expr: expression,
              },
            },
          },
        }}
      />
    )
    expect(screen.queryByText(expression)).not.toBeInTheDocument()
    expect(screen.getByText('$2')).toBeVisible()
    expect(screen.getByText('$8')).toBeVisible()
    expect(screen.getByText('$0')).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Copy billing expression' })
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: /details/i })
    ).not.toBeInTheDocument()
    const user = userEvent.setup()
    const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
    await user.click(screen.getByRole('button', { name: 'Copy billing expression' }))
    expect(copy).toHaveBeenCalledWith(expression)
  })

  it('shows every parsed tier and falls back to the full expression when pricing cannot be parsed safely', () => {
    const tiered = 'len <= 128000 ? tier("base", p * 2 + c * 8 + cr * 0.2) : tier("long", p * 4 + c * 12 + cr * 0.4)'
    const custom = 'tier("custom", p * 2 + c * 8) * max(1, param("factor"))'
    render(<TableFixture prices={{
      tiered: { current: {}, upstreams: { upstream: { billing_mode: 'tiered_expr', billing_expr: tiered } } },
      custom: { current: {}, upstreams: { upstream: { billing_mode: 'tiered_expr', billing_expr: custom } } },
    }} />)
    expect(screen.queryByText(tiered)).not.toBeInTheDocument()
    expect(screen.getByText(/128,000/)).toBeVisible()
    expect(screen.getByText('$0.2')).toBeVisible()
    expect(screen.getByText('$0.4')).toBeVisible()
    expect(screen.getByText(custom)).toBeVisible()
  })

  it('shows a mobile comparison list and preserves source-wide selection', async () => {
    const original = window.matchMedia
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({
      ...original(query),
      matches: query === '(max-width: 640px)',
    }))
    render(<TableFixture prices={prices} />)
    const user = userEvent.setup()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
    expect(screen.getByText('m')).toBeVisible()
    await user.click(
      screen.getByRole('checkbox', { name: 'Select all prices from upstream' })
    )
    const source = screen.getByRole('checkbox', {
      name: 'Select price for m from upstream',
    })
    expect(source).toBeChecked()
    source.focus()
    await user.keyboard(' ')
    expect(source).not.toBeChecked()
    expect(source).toHaveFocus()
  })

  it('selects one complete source per model and keeps filtered-out selections', async () => {
    const candidates: PricingSyncModels = {
      ...prices,
      z: {
        current: { model_ratio: 1, billing_mode: 'ratio' },
        upstreams: { upstream: { model_price: 0, billing_mode: 'ratio' } },
      },
    }
    candidates.m = {
      ...candidates.m,
      upstreams: {
        ...candidates.m.upstreams,
        alternative: { billing_mode: 'tiered_expr', billing_expr: expression },
      },
    }
    render(<TableFixture prices={candidates} />)
    const user = userEvent.setup()
    const first = screen.getByRole('checkbox', {
      name: 'Select price for m from upstream',
    })
    await user.click(first)
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Select price for m from alternative',
      })
    )
    expect(first).not.toBeChecked()
    expect(
      screen.getByRole('checkbox', {
        name: 'Select price for m from alternative',
      })
    ).toBeChecked()
    await user.click(
      screen.getByRole('checkbox', { name: 'Select all prices from upstream' })
    )
    expect(first).toBeChecked()
    expect(
      screen.getByRole('checkbox', { name: 'Select price for z from upstream' })
    ).toBeChecked()
    await user.type(screen.getByRole('textbox', { name: 'Search models' }), 'm')
    await waitFor(() =>
      expect(
        screen.queryByRole('checkbox', {
          name: 'Select price for z from upstream',
        })
      ).not.toBeInTheDocument()
    )
    await user.click(
      screen.getByRole('checkbox', { name: 'Select all prices from upstream' })
    )
    await user.clear(screen.getByRole('textbox', { name: 'Search models' }))
    expect(
      await screen.findByRole('checkbox', {
        name: 'Select price for z from upstream',
      })
    ).toBeChecked()
    expect(
      screen.getByRole('checkbox', { name: 'Select price for m from upstream' })
    ).not.toBeChecked()
  })

  it('replaces the selected pricing bundle without mixing legacy fields into expressions', () => {
    const options = pricingOptions({
      ModelRatio: '{"m":1,"untouched":2}',
      CompletionRatio: '{"m":2}',
      CacheRatio: '{"m":0.5}',
    })
    const applied = applyPriceSyncSelections(options, {
      m: {
        billing_mode: 'tiered_expr',
        billing_expr: expression,
        model_ratio: 9,
        model_price: 1,
      },
    })
    expect(pricingValuesByModel(applied).get('m')).toEqual({
      'billing_setting.billing_mode': 'tiered_expr',
      'billing_setting.billing_expr': expression,
    })
    expect(pricingValuesByModel(applied).get('untouched')).toEqual({
      ModelRatio: 2,
    })
    const legacy = applyPriceSyncSelections(applied, {
      m: { model_ratio: 1, completion_ratio: 4, cache_ratio: 0 },
    })
    expect(pricingValuesByModel(legacy).get('m')).toEqual({
      'billing_setting.billing_mode': 'ratio',
      ModelRatio: 1,
      CompletionRatio: 4,
      CacheRatio: 0,
    })
  })

  it('previews the expression, rejects a stale save and reloads before applying without residual ratio differences', async () => {
    let version = 'v1'
    const get = vi.spyOn(api, 'get').mockImplementation(async (url) => {
      if (url === '/api/ratio_sync/channels') {
        return {
          data: {
            success: true,
            data: [
              {
                id: 7,
                name: 'Upstream',
                base_url: 'https://example.test',
                type: 1,
                status: 1,
              },
            ],
          },
        }
      }
      return {
        data: {
          success: true,
          data: {
            entries: [
              {
                model_name: 'm',
                version,
                configured: { ModelRatio: 1, CompletionRatio: 2 },
                effective: { ModelRatio: 1, CompletionRatio: 2 },
              },
            ],
            empty_version: 'empty',
            options: pricingOptions({
              ModelRatio: '{"m":1,"untouched":2}',
              CompletionRatio: '{"m":2}',
            }),
          },
        },
      }
    })
    const post = vi.spyOn(api, 'post').mockResolvedValue({
      data: {
        success: true,
        data: {
          differences: {
            m: {
              billing_expr: {
                current: null,
                upstreams: { 'Upstream(7)': expression },
                confidence: { 'Upstream(7)': true },
              },
            },
          },
          prices: {
            m: {
              current: {
                model_ratio: 1,
                completion_ratio: 2,
                billing_mode: 'ratio',
              },
              upstreams: {
                'Upstream(7)': {
                  billing_mode: 'tiered_expr',
                  billing_expr: expression,
                },
              },
            },
          },
          test_results: [{ name: 'Upstream(7)', status: 'success' }],
        },
      },
    })
    const patch = vi
      .spyOn(api, 'patch')
      .mockResolvedValueOnce({
        data: {
          success: false,
          message: 'Model pricing changed; reload before saving',
        },
      })
      .mockResolvedValue({ data: { success: true } })
    const client = new QueryClient({
      defaultOptions: {
        queries: { retry: false },
        mutations: { retry: false },
      },
    })
    render(
      <QueryClientProvider client={client}>
        <UpstreamRatioSync />
      </QueryClientProvider>
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Select price sources' })
    )
    await user.click(
      await screen.findByRole('checkbox', { name: 'Select Upstream' })
    )
    await user.click(screen.getByRole('button', { name: 'Confirm Selection' }))
    await user.click(
      await screen.findByRole('checkbox', {
        name: 'Select price for m from Upstream(7)',
      })
    )
    expect(screen.getByText('1 selected models')).toBeVisible()
    await user.click(screen.getByRole('button', { name: 'Clear selection' }))
    expect(screen.getByRole('button', { name: 'Apply Sync' })).toBeDisabled()
    await user.click(
      screen.getByRole('checkbox', {
        name: 'Select price for m from Upstream(7)',
      })
    )
    await user.click(screen.getByRole('button', { name: 'Apply Sync' }))
    const preview = screen.getByRole('alertdialog', {
      name: 'Preview price changes',
    })
    expect(within(preview).getByText(/Expression pricing/)).toHaveTextContent('Input: $2')
    expect(within(preview).getByText(/Expression pricing/)).toHaveTextContent('Output: $8')
    expect(within(preview).queryByText(expression)).not.toBeInTheDocument()
    expect(patch).not.toHaveBeenCalled()
    await user.click(
      within(preview).getByRole('button', { name: 'Confirm Changes' })
    )
    expect(
      await screen.findByText('Model pricing changed; reload before saving')
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Confirm Changes' })
    ).toBeDisabled()
    version = 'v2'
    await user.click(screen.getByRole('button', { name: 'Reload pricing' }))
    await user.click(screen.getByRole('button', { name: 'Confirm Selection' }))
    await user.click(
      await screen.findByRole('checkbox', {
        name: 'Select price for m from Upstream(7)',
      })
    )
    await user.click(screen.getByRole('button', { name: 'Apply Sync' }))
    await user.click(screen.getByRole('button', { name: 'Confirm Changes' }))
    await waitFor(() =>
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    )
    expect(
      await screen.findByText('No upstream price differences found')
    ).toBeVisible()
    expect(patch).toHaveBeenLastCalledWith('/api/option/model_pricing', {
      changes: [
        {
          model_name: 'm',
          expected_version: 'v2',
          pricing: {
            'billing_setting.billing_mode': 'tiered_expr',
            'billing_setting.billing_expr': expression,
          },
        },
      ],
    })
    expect(post).toHaveBeenCalledTimes(2)
    expect(
      get.mock.calls.filter(([url]) => url === '/api/option/model_pricing')
        .length
    ).toBeGreaterThanOrEqual(2)
  })
})

it('shows a source failure instead of a no-differences result and retries the selected source', async () => {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/ratio_sync/channels') {
      return {
        data: {
          success: true,
          data: [
            {
              id: 7,
              name: 'Upstream',
              base_url: 'https://example.test',
              type: 1,
              status: 1,
            },
          ],
        },
      }
    }
    return {
      data: {
        success: true,
        data: {
          entries: [],
          empty_version: 'empty',
          options: pricingOptions({}),
        },
      },
    }
  })
  const post = vi
    .spyOn(api, 'post')
    .mockResolvedValueOnce({
      data: { success: false, message: 'Source unavailable' },
    })
    .mockResolvedValue({
      data: {
        success: true,
        data: {
          prices: {},
          differences: {},
          test_results: [{ name: 'Upstream(7)', status: 'success' }],
        },
      },
    })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <UpstreamRatioSync />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Select price sources' }))
  await user.click(
    await screen.findByRole('checkbox', { name: 'Select Upstream' })
  )
  await user.click(screen.getByRole('button', { name: 'Confirm Selection' }))
  expect(await screen.findByText('Source unavailable')).toBeVisible()
  expect(
    screen.queryByText('No upstream price differences found')
  ).not.toBeInTheDocument()
  expect(screen.getByRole('button', { name: 'Apply Sync' })).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Retry' }))
  expect(
    await screen.findByText('No upstream price differences found')
  ).toBeVisible()
  expect(post).toHaveBeenCalledTimes(2)
  expect(post.mock.calls[0]).toEqual(post.mock.calls[1])
})

it('preserves upstream expression text so a repeated comparison remains unchanged', () => {
  const source =
    'tier("base", p * 2 + c * 8) * (header("service-tier") == "fast" ? 2 : 1)'
  const after = applyPriceSyncSelections(pricingOptions({}), {
    model: { billing_mode: 'tiered_expr', billing_expr: source },
  })
  expect(
    pricingValuesByModel(after).get('model')?.['billing_setting.billing_expr']
  ).toBe(source)
})
