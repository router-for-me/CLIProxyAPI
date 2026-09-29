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
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { assert, describe, expect, test, vi } from 'vitest'

import { combineBillingExpr } from '@/features/pricing/lib/billing-expr'
import { evaluateBillingExpression } from '@/features/pricing/lib/billing-expression/runtime'

import { BillingConditionValueInput } from '../billing-time-fields'
import { TieredPricingEditor } from '../tiered-pricing-editor'

const expression =
  'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18))\n  ? tier("peak", p * 3 + cr * 0.10 + c * 9)\n  : tier("off_peak", p * 1.5 + cr * 0.05 + c * 4.5)'

const chainedExpression =
  'len <= 32000 && c <= 200 ? tier("discount", p * 0.8 + c * 2 + cr * 0.16 + cc * 0.17) : len <= 32000 ? tier("short", p * 0.8 + c * 8 + cr * 0.16 + cc * 0.17) : len <= 128000 ? tier("mid", p * 1.2 + c * 16 + cr * 0.16 + cc * 0.17) : tier("long", p * 2.4 + c * 24 + cr * 0.16 + cc * 0.17)'

test.each([
  ['zhCN', '星期一'],
  ['zhTW', '星期一'],
  ['en', 'Monday'],
  ['fr', 'lundi'],
  ['ru', 'понедельник'],
  ['ja', '月曜日'],
  ['vi', 'Thứ Hai'],
  [
    'invalid_locale',
    new Intl.DateTimeFormat(undefined, {
      weekday: 'long',
      timeZone: 'UTC',
    }).format(new Date('2026-01-05T00:00:00Z')),
  ],
])(
  'formats weekdays for %s and updates after a language switch',
  async (language, monday) => {
    const translations = createInstance()
    await translations.init({
      lng: language,
      fallbackLng: 'en',
      resources: Object.fromEntries(
        ['en', 'fr', language].map((code) => [
          code,
          { translation: { 'Condition value': 'Condition value' } },
        ])
      ),
    })
    const onChange = vi.fn()
    render(
      <I18nextProvider i18n={translations}>
        <BillingConditionValueInput
          probe='weekday'
          value='1'
          onChange={onChange}
        />
      </I18nextProvider>
    )
    expect(
      screen.getByRole('combobox', { name: 'Condition value' })
    ).toHaveTextContent(monday)
    await act(() => translations.changeLanguage('fr'))
    expect(
      screen.getByRole('combobox', { name: 'Condition value' })
    ).toHaveTextContent('lundi')
    expect(onChange).not.toHaveBeenCalled()
  }
)

test('shows chained tiers as peer rules and edits a later rule without changing precedence', async () => {
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={chainedExpression}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const rules = screen.getByRole('list', { name: 'Pricing rules' })
  expect(
    within(rules)
      .getAllByRole('listitem')
      .filter((item) => item.parentElement === rules)
  ).toHaveLength(4)
  const short = within(
    screen.getByRole('group', { name: 'Pricing tier short' })
  )
  expect(
    short.queryByRole('textbox', { name: 'Output price' })
  ).not.toBeInTheDocument()
  const expand = short.getByRole('button', { name: 'Edit pricing rule short' })
  expand.focus()
  const user = userEvent.setup()
  await user.keyboard('{Enter}')
  expect(expand).toHaveAttribute('aria-expanded', 'true')
  expect(short.getByRole('textbox', { name: 'Output price' })).toHaveValue('8')
  expect(onBillingExprChange).not.toHaveBeenCalled()
  fireEvent.change(short.getByRole('textbox', { name: 'Output price' }), {
    target: { value: '10' },
  })
  fireEvent.change(short.getByRole('textbox', { name: 'Condition value' }), {
    target: { value: '40000' },
  })
  expect(onBillingExprChange).toHaveBeenLastCalledWith(
    chainedExpression
      .replace('c * 8', 'c * 10')
      .replace(': len <= 32000', ': len <= 40000')
  )
  const generated = onBillingExprChange.mock.lastCall?.[0]
  assert(generated)
  for (const [len, c, matchedTier] of [
    [32000, 200, 'discount'],
    [32000, 201, 'short'],
    [35000, 100, 'short'],
    [40001, 100, 'mid'],
    [128001, 100, 'long'],
  ] as const) {
    expect(
      evaluateBillingExpression(generated, {
        tokens: { len, p: len, c, cr: 0, cc: 0 },
      })
    ).toMatchObject({ status: 'success', matchedTier })
  }
  await user.click(expand)
  expect(expand).toHaveAttribute('aria-expanded', 'false')
  expect(expand).toHaveTextContent('Output: $10')
  await user.click(expand)
  expect(short.getByRole('textbox', { name: 'Output price' })).toHaveValue('10')
  expect(short.getByRole('textbox', { name: 'Condition value' })).toHaveValue(
    '40000'
  )
  await user.click(short.getByRole('button', { name: 'Branch actions 2' }))
  await user.click(screen.getByRole('menuitem', { name: 'Remove branch' }))
  const removed = onBillingExprChange.mock.lastCall?.[0]
  assert(removed)
  expect(removed).not.toContain('tier("short"')
  expect(
    evaluateBillingExpression(removed, {
      tokens: { len: 35000, p: 35000, c: 100, cr: 0, cc: 0 },
    })
  ).toMatchObject({ status: 'success', matchedTier: 'mid', cost: 43600 })
})

test('retains all pricing inputs and switches inside an expanded rule', async () => {
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={chainedExpression}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const user = userEvent.setup()
  const tier = within(screen.getByRole('group', { name: 'Pricing tier short' }))
  await user.click(
    tier.getByRole('button', { name: 'Edit pricing rule short' })
  )
  await user.click(tier.getByRole('button', { name: 'Media pricing' }))
  for (const label of [
    'Input price',
    'Output price',
    'Cache read price',
    'Cache create price',
    'Cache create (1h) price',
    'Image input price',
    'Image cache input price',
    'Image output price',
    'Audio input price',
    'Audio output price',
  ]) {
    expect(tier.getByRole('textbox', { name: label })).toBeVisible()
    expect(
      tier.getByRole('checkbox', { name: `Include ${label}` })
    ).toBeVisible()
  }
  await user.click(
    tier.getByRole('checkbox', { name: 'Include Cache create (1h) price' })
  )
  fireEvent.change(
    tier.getByRole('textbox', { name: 'Cache create (1h) price' }),
    { target: { value: '0.3' } }
  )
  await user.click(
    tier.getByRole('checkbox', { name: 'Include Image cache input price' })
  )
  expect(onBillingExprChange.mock.lastCall?.[0]).toContain('img_cr * 0')
  expect(onBillingExprChange.mock.lastCall?.[0]).toContain('cc1h * 0.3')
  await user.click(tier.getByRole('combobox', { name: 'Tier billing mode' }))
  await user.click(screen.getByRole('option', { name: 'Per-call' }))
  fireEvent.change(tier.getByRole('textbox', { name: 'Price per request' }), {
    target: { value: '0.02' },
  })
  expect(onBillingExprChange.mock.lastCall?.[0]).toContain(
    'tier("short", fixed(0.02))'
  )
  await user.click(tier.getByRole('combobox', { name: 'Tier billing mode' }))
  await user.click(screen.getByRole('option', { name: 'Per token' }))
  expect(
    tier.getByRole('textbox', { name: 'Cache create (1h) price' })
  ).toHaveValue('0.3')
  expect(
    tier.getByRole('checkbox', { name: 'Include Image cache input price' })
  ).toBeChecked()
  expect(tier.getByRole('button', { name: 'Add pricing branch' })).toBeVisible()
  expect(tier.getByRole('textbox', { name: 'Tier name' })).toHaveValue('short')
  await user.click(
    tier.getByRole('button', { name: 'Edit pricing rule short' })
  )
  await user.click(
    tier.getByRole('button', { name: 'Edit pricing rule short' })
  )
  expect(
    tier.getByRole('textbox', { name: 'Image cache input price' })
  ).toBeVisible()
  expect(
    tier.getByRole('checkbox', { name: 'Include Image cache input price' })
  ).toBeChecked()
})

test.each([
  '',
  'tier("base", p * 3.00 + c * 9)',
  'tier("request", fixed(0.0100))',
  'len < 200000 ? tier("short", p * 3 + c * 9) : tier("long", p * 6 + c * 18)',
])(
  'uses condition trees by default without changing pricing: %s',
  async (source) => {
    const onBillingExprChange = vi.fn()
    const onRequestRuleExprChange = vi.fn()
    const rule = '(header("x-plan") == "fast" ? 2.00 : 1)'
    render(
      <TieredPricingEditor
        billingExpr={source}
        requestRuleExpr={rule}
        onBillingExprChange={onBillingExprChange}
        onRequestRuleExprChange={onRequestRuleExprChange}
      />
    )
    const user = userEvent.setup()
    expect(
      screen.getAllByRole('button', { name: 'Add pricing branch' }).length
    ).toBeGreaterThan(0)
    await user.click(screen.getByRole('combobox', { name: 'Editor mode' }))
    await user.click(screen.getByRole('option', { name: 'Expression editor' }))
    expect(
      screen.getByRole('textbox', { name: 'Billing expression' })
    ).toHaveValue(combineBillingExpr(source, rule))
    await user.click(screen.getByRole('combobox', { name: 'Editor mode' }))
    await user.click(screen.getByRole('option', { name: 'Visual editor' }))
    expect(
      screen.getAllByRole('button', { name: 'Add pricing branch' }).length
    ).toBeGreaterThan(0)
    expect(onBillingExprChange).not.toHaveBeenCalled()
    expect(onRequestRuleExprChange).not.toHaveBeenCalled()
  }
)

test('keeps the condition tree as the default after switching models and applying presets', async () => {
  const props = {
    requestRuleExpr: '',
    onBillingExprChange: vi.fn(),
    onRequestRuleExprChange: vi.fn(),
  }
  const view = render(
    <TieredPricingEditor
      {...props}
      modelName='timed'
      billingExpr={expression}
    />
  )
  view.rerender(
    <TieredPricingEditor
      {...props}
      modelName='simple'
      billingExpr='tier("base", p * 2 + c * 8)'
    />
  )
  expect(
    screen.getByRole('button', { name: 'Add pricing branch' })
  ).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Input price' })).toHaveValue('2')
  expect(props.onBillingExprChange).not.toHaveBeenCalled()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Flat' }))
  expect(
    screen.getByRole('button', { name: 'Add pricing branch' })
  ).toBeVisible()
  expect(screen.getByRole('textbox', { name: 'Output price' })).toHaveValue('4')
  expect(props.onBillingExprChange).toHaveBeenLastCalledWith(
    'tier("base", p * 2 + c * 4)'
  )
})

test('builds weekday peak pricing with two time ranges from an empty visual form', async () => {
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr=''
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const user = userEvent.setup()
  fireEvent.change(screen.getByRole('textbox', { name: 'Tier name' }), {
    target: { value: '空闲' },
  })
  fireEvent.change(screen.getByRole('textbox', { name: 'Input price' }), {
    target: { value: '1.5' },
  })
  fireEvent.change(screen.getByRole('textbox', { name: 'Output price' }), {
    target: { value: '4.5' },
  })
  await user.click(
    screen.getByRole('checkbox', { name: 'Include Cache read price' })
  )
  fireEvent.change(screen.getByRole('textbox', { name: 'Cache read price' }), {
    target: { value: '0.05' },
  })
  await user.click(screen.getByRole('button', { name: 'Add pricing branch' }))
  fireEvent.change(screen.getAllByRole('textbox', { name: 'Tier name' })[0], {
    target: { value: '高峰' },
  })
  const peak = within(screen.getByRole('group', { name: 'Pricing tier 高峰' }))
  fireEvent.change(peak.getByRole('textbox', { name: 'Input price' }), {
    target: { value: '3' },
  })
  fireEvent.change(peak.getByRole('textbox', { name: 'Output price' }), {
    target: { value: '9' },
  })
  fireEvent.change(peak.getByRole('textbox', { name: 'Cache read price' }), {
    target: { value: '0.10' },
  })

  await user.click(screen.getByRole('combobox', { name: 'Condition input' }))
  await user.click(screen.getByRole('option', { name: 'Weekday' }))
  await user.click(screen.getByRole('combobox', { name: 'Condition value' }))
  await user.click(screen.getByRole('option', { name: 'Monday' }))
  await user.click(screen.getByRole('button', { name: 'Condition actions 1' }))
  await user.click(screen.getByRole('menuitem', { name: 'Add condition' }))
  await user.click(
    screen.getAllByRole('combobox', { name: 'Condition input' })[1]
  )
  await user.click(screen.getByRole('option', { name: 'Weekday' }))
  await user.click(
    screen.getAllByRole('combobox', { name: 'Condition value' })[1]
  )
  await user.click(screen.getByRole('option', { name: 'Friday' }))
  await user.click(
    screen.getAllByRole('combobox', { name: 'Comparison operator' })[1]
  )
  await user.click(screen.getByRole('option', { name: '<=' }))
  await user.click(screen.getByRole('button', { name: 'Add to group 1' }))
  await user.click(
    screen.getByRole('menuitem', { name: 'Add condition group' })
  )

  for (const [index, start, end] of [
    [1, 9, 12],
    [2, 14, 18],
  ]) {
    await user.click(screen.getByRole('button', { name: 'Add to group 1.2' }))
    await user.click(
      screen.getByRole('menuitem', { name: 'Add condition group' })
    )
    const period = within(
      screen.getByRole('group', { name: `Condition group 1.2.${index}` })
    )
    await user.click(period.getByRole('combobox', { name: 'Condition group' }))
    await user.click(screen.getByRole('option', { name: 'All conditions' }))
    await user.click(
      screen.getByRole('button', { name: `Add to group 1.2.${index}` })
    )
    await user.click(screen.getByRole('menuitem', { name: 'Add condition' }))
    fireEvent.change(period.getByRole('textbox', { name: 'Condition value' }), {
      target: { value: String(start) },
    })
    await user.click(
      screen.getByRole('button', { name: `Add to group 1.2.${index}` })
    )
    await user.click(screen.getByRole('menuitem', { name: 'Add condition' }))
    fireEvent.change(
      period.getAllByRole('textbox', { name: 'Condition value' })[1],
      { target: { value: String(end) } }
    )
    await user.click(
      period.getAllByRole('combobox', { name: 'Comparison operator' })[1]
    )
    await user.click(screen.getByRole('option', { name: '<' }))
  }

  const generated = onBillingExprChange.mock.lastCall?.[0]
  assert(generated)
  for (const [localTime, matchedTier, cost] of [
    ['2026-09-07T08:59:00', '空闲', 600.5],
    ['2026-09-07T09:00:00', '高峰', 1201],
    ['2026-09-07T12:00:00', '空闲', 600.5],
    ['2026-09-07T14:00:00', '高峰', 1201],
    ['2026-09-07T18:00:00', '空闲', 600.5],
    ['2026-09-11T10:00:00', '高峰', 1201],
    ['2026-09-12T10:00:00', '空闲', 600.5],
    ['2026-09-13T15:00:00', '空闲', 600.5],
  ] as const) {
    expect(
      evaluateBillingExpression(generated, {
        now: new Date(`${localTime}+08:00`),
        tokens: { p: 100, c: 100, cr: 10 },
      })
    ).toMatchObject({ status: 'success', matchedTier, cost })
  }
})

test('edits image cache pricing and preserves an explicitly free cache lane', () => {
  const source =
    'tier("standard", p * 5 + c * 30 + cr * 1.25 + img * 8 + img_cr * 2)'
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={source}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const price = screen.getByRole('textbox', { name: 'Image cache input price' })
  expect(price).toHaveValue('2')
  fireEvent.change(price, { target: { value: '0' } })
  expect(onBillingExprChange.mock.lastCall?.[0]).toContain('img_cr * 0')
  expect(
    screen.getByRole('checkbox', { name: 'Include Image cache input price' })
  ).toBeChecked()
})

describe('visual time billing editor', () => {
  test.each([
    ['simple tiers', 'tier("base", p * 2 + c * 8)', '2'],
    ['condition tree', expression, '3'],
  ])(
    'switches %s between token and request prices while preserving drafts',
    async (_name, source, tokenPrice) => {
      const onBillingExprChange = vi.fn()
      const onRequestRuleExprChange = vi.fn()
      render(
        <TieredPricingEditor
          billingExpr={source}
          requestRuleExpr='(param("fast") == true ? 2 : 1)'
          onBillingExprChange={onBillingExprChange}
          onRequestRuleExprChange={onRequestRuleExprChange}
        />
      )
      const user = userEvent.setup()
      await user.click(
        screen.getAllByRole('combobox', { name: 'Tier billing mode' })[0]
      )
      await user.click(screen.getByRole('option', { name: 'Per-call' }))
      expect(
        screen.getByRole('textbox', { name: 'Price per request' })
      ).toHaveValue('')
      expect(screen.getAllByRole('alert').length).toBeGreaterThan(0)
      expect(onBillingExprChange).not.toHaveBeenCalled()
      fireEvent.change(
        screen.getByRole('textbox', { name: 'Price per request' }),
        { target: { value: '0.02' } }
      )
      expect(onBillingExprChange.mock.lastCall?.[0]).toContain('fixed(0.02)')
      await user.click(
        screen.getAllByRole('combobox', { name: 'Tier billing mode' })[0]
      )
      await user.click(screen.getByRole('option', { name: 'Per token' }))
      expect(
        screen.getAllByRole('textbox', { name: 'Input price' })[0]
      ).toHaveValue(tokenPrice)
      await user.click(
        screen.getAllByRole('combobox', { name: 'Tier billing mode' })[0]
      )
      await user.click(screen.getByRole('option', { name: 'Per-call' }))
      expect(
        screen.getByRole('textbox', { name: 'Price per request' })
      ).toHaveValue('0.02')
      fireEvent.change(
        screen.getByRole('textbox', { name: 'Price per request' }),
        { target: { value: '0' } }
      )
      expect(onBillingExprChange.mock.lastCall?.[0]).toContain('fixed(0)')
      expect(onRequestRuleExprChange).not.toHaveBeenCalled()
    }
  )
  test('converts request price input currency without rewriting untouched USD prices', () => {
    const onBillingExprChange = vi.fn()
    const props = {
      billingExpr: 'tier("request", fixed(0.0100))',
      requestRuleExpr: '',
      onBillingExprChange,
      onRequestRuleExprChange: vi.fn(),
    }
    const { rerender } = render(
      <TieredPricingEditor
        {...props}
        currency={{ label: 'CNY', symbol: '¥', exchangeRate: 7 }}
      />
    )
    expect(
      screen.getByRole('textbox', { name: 'Price per request' })
    ).toHaveValue('0.07')
    expect(onBillingExprChange).not.toHaveBeenCalled()
    fireEvent.change(
      screen.getByRole('textbox', { name: 'Price per request' }),
      { target: { value: '0.14' } }
    )
    expect(onBillingExprChange).toHaveBeenLastCalledWith(
      'tier("request", fixed(0.02))'
    )
    rerender(
      <TieredPricingEditor
        {...props}
        currency={{ label: 'USD', symbol: '$', exchangeRate: 1 }}
      />
    )
    expect(
      screen.getByRole('textbox', { name: 'Price per request' })
    ).toHaveValue('0.02')
    expect(onBillingExprChange).toHaveBeenCalledTimes(1)
  })
  test('opens mixed request and token prices visually without rewriting the expression', () => {
    const onBillingExprChange = vi.fn()
    render(
      <TieredPricingEditor
        billingExpr='len <= 32000 ? tier("short", fixed(0.01)) : tier("long", p * 2 + c * 8)'
        requestRuleExpr=''
        onBillingExprChange={onBillingExprChange}
        onRequestRuleExprChange={vi.fn()}
      />
    )
    const tier = screen.getByRole('group', { name: 'Pricing tier short' })
    expect(
      within(tier).getByRole('textbox', { name: 'Price per request' })
    ).toHaveValue('0.01')
    expect(
      within(tier).queryByRole('textbox', { name: 'Input price' })
    ).not.toBeInTheDocument()
    expect(onBillingExprChange).not.toHaveBeenCalled()
  })
  test('opens group actions by keyboard and returns focus when dismissed', async () => {
    const onBillingExprChange = vi.fn()
    render(
      <TieredPricingEditor
        billingExpr={expression}
        requestRuleExpr=''
        onBillingExprChange={onBillingExprChange}
        onRequestRuleExprChange={vi.fn()}
      />
    )
    const user = userEvent.setup()
    const actions = screen.getByRole('button', {
      name: 'Condition actions 1.2',
    })
    actions.focus()
    await user.keyboard('{Enter}')
    expect(
      screen.getByRole('menuitem', { name: 'Negate condition' })
    ).toBeVisible()
    await user.keyboard('{Escape}')
    expect(actions).toHaveFocus()
    expect(onBillingExprChange).not.toHaveBeenCalled()
  })

  test('negates the merged weekday row without changing the surrounding time group', async () => {
    const onBillingExprChange = vi.fn()
    render(
      <TieredPricingEditor
        billingExpr={expression}
        requestRuleExpr=''
        onBillingExprChange={onBillingExprChange}
        onRequestRuleExprChange={vi.fn()}
      />
    )
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Condition actions 1.1' })
    )
    await user.click(screen.getByRole('menuitem', { name: 'Negate condition' }))
    expect(
      screen.getByRole('group', { name: 'Condition group 1.1' })
    ).toHaveAttribute('data-condition-kind', 'not')
    const generated = onBillingExprChange.mock.lastCall?.[0]
    assert(generated)
    for (const [now, matchedTier] of [
      ['2026-09-12T10:00:00+08:00', 'peak'],
      ['2026-09-08T10:00:00+08:00', 'off_peak'],
      ['2026-09-12T13:00:00+08:00', 'off_peak'],
    ]) {
      expect(
        evaluateBillingExpression(generated, {
          now: new Date(now),
          tokens: { p: 100, c: 0, cr: 0 },
        })
      ).toMatchObject({ status: 'success', matchedTier })
    }
  })
  test('exposes the nested group hierarchy and merges weekday bounds without rewriting the source', () => {
    const onBillingExprChange = vi.fn()
    render(
      <TieredPricingEditor
        billingExpr={expression}
        requestRuleExpr=''
        onBillingExprChange={onBillingExprChange}
        onRequestRuleExprChange={vi.fn()}
      />
    )
    const root = screen.getByRole('group', { name: 'Condition group 1' })
    const periods = within(root).getByRole('group', {
      name: 'Condition group 1.2',
    })
    expect(root).toHaveAttribute('data-condition-kind', 'all')
    expect(periods).toHaveAttribute('data-condition-kind', 'any')
    expect(
      within(periods).getByRole('group', { name: 'Condition group 1.2.1' })
    ).toHaveAttribute('data-condition-kind', 'all')
    expect(
      within(root).getByRole('combobox', { name: 'Start weekday' })
    ).toHaveTextContent('Monday')
    expect(
      within(root).getByRole('combobox', { name: 'End weekday' })
    ).toHaveTextContent('Friday')
    expect(
      screen.queryByRole('button', { name: 'Negate condition' })
    ).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Edit pricing rule peak' })
    ).toHaveAttribute('aria-expanded', 'true')
    expect(
      screen.getByRole('button', { name: 'Edit pricing rule off_peak' })
    ).toHaveTextContent('No preceding rule matched')
    expect(onBillingExprChange).not.toHaveBeenCalled()
  })
  test('opens the complete time expression visually without publishing changes', async () => {
    const onBillingExprChange = vi.fn()
    render(
      <TieredPricingEditor
        billingExpr={expression}
        requestRuleExpr=''
        onBillingExprChange={onBillingExprChange}
        onRequestRuleExprChange={vi.fn()}
      />
    )
    expect(
      within(
        screen.getByRole('group', { name: 'Pricing tier peak' })
      ).getByRole('textbox', { name: 'Input price' })
    ).toHaveValue('3')
    const user = userEvent.setup()
    await user.click(
      screen.getByRole('button', { name: 'Edit pricing rule off_peak' })
    )
    expect(
      within(
        screen.getByRole('group', { name: 'Pricing tier off_peak' })
      ).getByRole('textbox', { name: 'Input price' })
    ).toHaveValue('1.5')
    expect(
      screen.getAllByRole('combobox', { name: 'Condition group' }).length
    ).toBeGreaterThan(1)
    expect(onBillingExprChange).not.toHaveBeenCalled()
  })
})

test('keeps exact source and independent request rules through mode and currency changes', async () => {
  const onBillingExprChange = vi.fn()
  const onRequestRuleExprChange = vi.fn()
  const rule = '(header("x-plan") == "fast" ? 2.00 : 1)'
  const props = {
    billingExpr: expression,
    requestRuleExpr: rule,
    onBillingExprChange,
    onRequestRuleExprChange,
  }
  const view = render(<TieredPricingEditor {...props} />)
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Editor mode' }))
  await user.click(screen.getByRole('option', { name: 'Expression editor' }))
  expect(
    screen.getByRole('textbox', { name: 'Billing expression' })
  ).toHaveValue(combineBillingExpr(expression, rule))
  await user.click(screen.getByRole('combobox', { name: 'Editor mode' }))
  await user.click(screen.getByRole('option', { name: 'Visual editor' }))
  view.rerender(
    <TieredPricingEditor
      {...props}
      currency={{ label: 'CNY', symbol: '¥', exchangeRate: 7 }}
    />
  )
  expect(
    within(screen.getByRole('group', { name: 'Pricing tier peak' })).getByRole(
      'textbox',
      { name: 'Input price' }
    )
  ).toHaveValue('21')
  expect(screen.getAllByRole('textbox', { name: 'Start' })[0]).toHaveValue('9')
  expect(onBillingExprChange).not.toHaveBeenCalled()
  expect(onRequestRuleExprChange).not.toHaveBeenCalled()
  fireEvent.change(
    within(screen.getByRole('group', { name: 'Pricing tier peak' })).getByRole(
      'textbox',
      { name: 'Input price' }
    ),
    { target: { value: '28' } }
  )
  expect(onBillingExprChange).toHaveBeenLastCalledWith(
    expression.replace('p * 3', 'p * 4')
  )
  expect(onRequestRuleExprChange).not.toHaveBeenCalled()
})

test('updates weekday, hour and timezone conditions while preserving the other branches', async () => {
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={expression}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Start weekday' }))
  await user.click(screen.getByRole('option', { name: 'Tuesday' }))
  expect(onBillingExprChange).toHaveBeenLastCalledWith(
    expression.replace('>= 1', '>= 2')
  )
  fireEvent.change(screen.getAllByRole('textbox', { name: 'Start' })[0], {
    target: { value: '10' },
  })
  const changed = expression.replace('>= 1', '>= 2').replace('>= 9', '>= 10')
  expect(onBillingExprChange).toHaveBeenLastCalledWith(changed)
  expect(
    evaluateBillingExpression(changed, {
      now: new Date('2026-09-08T09:00:00+08:00'),
      tokens: { p: 100, c: 0, cr: 0 },
    })
  ).toMatchObject({ status: 'success', matchedTier: 'off_peak', cost: 150 })
  fireEvent.change(screen.getAllByRole('combobox', { name: 'Timezone' })[0], {
    target: { value: 'UTC' },
  })
  expect(onBillingExprChange).toHaveBeenLastCalledWith(
    changed.replaceAll('weekday("Asia/Shanghai")', 'weekday("UTC")')
  )
})

test('keeps incomplete prices and groups local, and publishes only after correction', async () => {
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={expression}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const price = within(
    screen.getByRole('group', { name: 'Pricing tier peak' })
  ).getByRole('textbox', { name: 'Input price' })
  fireEvent.change(price, { target: { value: '' } })
  expect(price).toHaveAttribute('aria-invalid', 'true')
  expect(screen.getByText('Enter a finite, non-negative price.')).toBeVisible()
  expect(onBillingExprChange).not.toHaveBeenCalled()
  fireEvent.change(price, { target: { value: '3' } })
  onBillingExprChange.mockClear()
  const user = userEvent.setup()
  await user.click(screen.getByRole('button', { name: 'Add to group 1' }))
  await user.click(
    screen.getByRole('menuitem', { name: 'Add condition group' })
  )
  expect(
    screen.getByText('Add at least one condition to this group.')
  ).toBeVisible()
  expect(onBillingExprChange).not.toHaveBeenCalled()
  await user.click(screen.getByRole('combobox', { name: 'Editor mode' }))
  expect(
    screen.getByRole('option', { name: 'Expression editor' })
  ).toHaveAttribute('aria-disabled', 'true')
})

test('copies prices into a new branch and retains the original as its otherwise tier', async () => {
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={expression}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const user = userEvent.setup()
  await user.click(
    within(screen.getByRole('group', { name: 'Pricing tier peak' })).getByRole(
      'button',
      { name: 'Add pricing branch' }
    )
  )
  expect(
    screen.getAllByRole('group', { name: 'Pricing tier peak' })
  ).toHaveLength(2)
  for (const tier of screen.getAllByRole('group', {
    name: 'Pricing tier peak',
  })) {
    const expand = within(tier).getByRole('button', {
      name: 'Edit pricing rule peak',
    })
    if (expand.getAttribute('aria-expanded') === 'false') {
      await user.click(expand)
    }
    expect(
      within(tier).getByRole('textbox', { name: 'Input price' })
    ).toHaveValue('3')
  }
  expect(onBillingExprChange).not.toHaveBeenCalled()
  const empty = screen
    .getAllByRole('textbox', { name: 'Condition value' })
    .find((input) => (input as HTMLInputElement).value === '')
  assert(empty)
  fireEvent.change(empty, { target: { value: '10' } })
  const lastCall = onBillingExprChange.mock.lastCall
  assert(lastCall)
  const generated = lastCall[0]
  expect(
    evaluateBillingExpression(generated, {
      now: new Date('2026-09-07T09:00:00+08:00'),
      tokens: { p: 100, c: 0, cr: 0 },
    })
  ).toMatchObject({ status: 'success', matchedTier: 'peak', cost: 300 })
  expect(
    evaluateBillingExpression(generated, {
      now: new Date('2026-09-07T11:00:00+08:00'),
      tokens: { p: 100, c: 0, cr: 0 },
    })
  ).toMatchObject({ status: 'success', matchedTier: 'peak', cost: 300 })
})

test('preserves explicit cache zero in the document form even for an otherwise legacy-shaped tier', () => {
  const source = 'tier("base", p * 3 + c * 9 + cr * 0)'
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={source}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const tier = within(screen.getByRole('group', { name: 'Pricing tier base' }))
  expect(
    tier.getByRole('checkbox', { name: 'Include Cache read price' })
  ).toBeChecked()
  fireEvent.change(tier.getByRole('textbox', { name: 'Input price' }), {
    target: { value: '4' },
  })
  expect(onBillingExprChange).toHaveBeenLastCalledWith(
    source.replace('p * 3', 'p * 4')
  )
})

test.each([
  'hour("UTC") >= 9 ? max(p * 2, 100) : tier("off", p * 1)',
  'v2:tier("base", p * 3 + c * 9)',
])(
  'leaves unsupported expressions in raw mode without publishing a default price: %s',
  async (source) => {
    const onBillingExprChange = vi.fn()
    render(
      <TieredPricingEditor
        billingExpr={source}
        requestRuleExpr=''
        onBillingExprChange={onBillingExprChange}
        onRequestRuleExprChange={vi.fn()}
      />
    )
    const user = userEvent.setup()
    await user.click(screen.getByRole('combobox', { name: 'Editor mode' }))
    await user.click(screen.getByRole('option', { name: 'Visual editor' }))
    expect(screen.getByDisplayValue(source)).toBeVisible()
    expect(onBillingExprChange).not.toHaveBeenCalled()
  }
)

test('changes logical groups, negation and branch removal through the visual controls', async () => {
  const source =
    'hour("UTC") >= 9 && hour("UTC") < 18 ? tier("on", p * 1) : tier("off", p * 2)'
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={source}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('combobox', { name: 'Condition group' }))
  await user.click(screen.getByRole('option', { name: 'Any condition' }))
  await user.click(screen.getByRole('button', { name: 'Condition actions 1' }))
  await user.click(screen.getByRole('menuitem', { name: 'Negate condition' }))
  const negated = onBillingExprChange.mock.lastCall?.[0]
  assert(negated)
  expect(
    evaluateBillingExpression(negated, {
      now: new Date('2026-09-07T10:00:00Z'),
      tokens: { p: 100 },
    })
  ).toMatchObject({ status: 'success', matchedTier: 'off', cost: 200 })
  await user.click(screen.getByRole('button', { name: 'Condition actions 1' }))
  await user.click(screen.getByRole('menuitem', { name: 'Remove negation' }))
  await user.click(screen.getByRole('button', { name: 'Condition actions 1' }))
  await user.click(
    screen.getByRole('menuitem', { name: 'Remove start condition' })
  )
  expect(screen.getByRole('textbox', { name: 'Condition value' })).toHaveValue(
    '18'
  )
  await user.click(screen.getByRole('button', { name: 'Branch actions 1' }))
  await user.click(screen.getByRole('menuitem', { name: 'Remove branch' }))
  expect(onBillingExprChange).toHaveBeenLastCalledWith('tier("off", p * 2)')
})

test('keeps an invalid timezone local until it is corrected', () => {
  const onBillingExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr={expression}
      requestRuleExpr=''
      onBillingExprChange={onBillingExprChange}
      onRequestRuleExprChange={vi.fn()}
    />
  )
  const timezone = screen.getAllByRole('combobox', { name: 'Timezone' })[0]
  fireEvent.change(timezone, { target: { value: 'Asia/' } })
  expect(timezone).toHaveAttribute('aria-invalid', 'true')
  expect(screen.getByText('Choose a valid IANA timezone.')).toBeVisible()
  expect(onBillingExprChange).not.toHaveBeenCalled()
  fireEvent.change(timezone, { target: { value: 'Asia/Tokyo' } })
  expect(timezone).not.toHaveAttribute('aria-invalid')
  expect(onBillingExprChange).toHaveBeenLastCalledWith(
    expression.replaceAll('weekday("Asia/Shanghai")', 'weekday("Asia/Tokyo")')
  )
})

test('preserves legacy numeric drafts when editing an independent time multiplier range', () => {
  const onRequestRuleExprChange = vi.fn()
  render(
    <TieredPricingEditor
      billingExpr='tier("base", p * 3 + c * 9)'
      requestRuleExpr='(hour("Asia/Shanghai") >= 21 || hour("Asia/Shanghai") < 6 ? 0.5 : 1)'
      onBillingExprChange={vi.fn()}
      onRequestRuleExprChange={onRequestRuleExprChange}
    />
  )
  const start = screen.getByRole('spinbutton', { name: 'Start' })
  fireEvent.focus(start)
  fireEvent.change(start, { target: { value: '' } })
  expect(start).toHaveValue(null)
  expect(onRequestRuleExprChange.mock.lastCall?.[0]).toContain('>= 0')
  fireEvent.blur(start)
  expect(start).toHaveValue(0)
})
