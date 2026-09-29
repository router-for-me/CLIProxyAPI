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
import assert from 'node:assert/strict'

import { describe, expect, test } from 'vitest'

import { parseTiersFromExpr } from '../lib/billing-expr'
import { getBillingModeLabelKey } from '../lib/billing-mode'
import {
  getCardExamplePrice,
  getDynamicPriceUnitLabelKey,
  getDynamicPricingSummary,
  getDynamicPricingTiers,
  getTaskUsagePriceUnitLabelKey,
  hasTaskUsageSchema,
  isUnconfiguredTaskUsageModel,
} from '../lib/dynamic-price'
import { isTokenBasedModel } from '../lib/model-helpers'
import type { PricingModel } from '../types'

function pricingModel(overrides: Partial<PricingModel>): PricingModel {
  return {
    id: 1,
    model_name: 'test-model',
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 1,
    enable_groups: ['default'],
    ...overrides,
  }
}

const summaryOptions = {
  tokenUnit: 'K' as const,
  showRechargePrice: true,
  priceRate: 3,
  usdExchangeRate: 6,
  groupRatioMultiplier: 2,
}

describe('expression price summaries', () => {
  test('keeps request prices unchanged by token units and separates mixed billing units', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr:
        'len < 1000 ? tier("request", fixed(0.01)) : tier("tokens", p * 2 + c * 8)',
    })
    const thousand = getDynamicPricingSummary(model, { tokenUnit: 'K' })
    const million = getDynamicPricingSummary(model, { tokenUnit: 'M' })
    const request = thousand?.primaryEntries.find(
      (entry) => entry.unit === 'request'
    )
    expect(request?.value).toBe(0.01)
    expect(request?.formatted).toBe(
      million?.primaryEntries.find((entry) => entry.unit === 'request')
        ?.formatted
    )
    expect(thousand?.primaryEntries.map((entry) => entry.unit)).toEqual([
      'token',
      'token',
      'request',
    ])
    expect(thousand?.isMixedBilling).toBe(true)
    expect(
      thousand?.primaryEntries.every(
        (entry) => entry.formattedRange === undefined
      )
    ).toBe(true)
    const free = getDynamicPricingSummary(
      pricingModel({
        billing_mode: 'tiered_expr',
        billing_expr: 'tier("free", fixed(0))',
      }),
      { tokenUnit: 'M' }
    )
    expect(free?.primaryEntries).toHaveLength(1)
    expect(free?.primaryEntries[0].value).toBe(0)
  })
  const timeExpression =
    'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18)) ? tier("peak", p * 3 + cr * 0.1 + c * 9) : tier("off_peak", p * 1.5 + cr * 0.05 + c * 4.5)'

  test.each([
    ['2026-09-07T09:00:00+08:00', 'peak', [3, 9]],
    ['2026-09-07T12:00:00+08:00', 'off_peak', [1.5, 4.5]],
    ['2026-09-12T09:00:00+08:00', 'off_peak', [1.5, 4.5]],
  ])(
    'adds current-period selection at %s without removing detail rows',
    (now, tier, prices) => {
      const summary = getDynamicPricingSummary(
        pricingModel({
          billing_mode: 'tiered_expr',
          billing_expr: timeExpression,
        }),
        { tokenUnit: 'M', now: new Date(now) }
      )
      expect(summary?.tier?.label).toBe(tier)
      expect(summary?.primaryEntries.map((entry) => entry.value)).toEqual(
        prices
      )
      expect(summary?.tiers.map((entry) => entry.label)).toEqual([
        'peak',
        'off_peak',
      ])
      expect(summary?.isTimePricing).toBe(true)
      expect(
        parseTiersFromExpr(timeExpression).every((entry) =>
          entry.conditionText?.includes('Asia/Shanghai')
        )
      ).toBe(true)
    }
  )

  test('keeps the first token tier inside a time branch and leaves request multipliers separate', () => {
    const base =
      'hour("UTC") < 12 ? (len < 1000 ? tier("first", p * 4 + c * 12) : tier("cheaper", p * 2 + c * 6)) : tier("night", p * 1 + c * 3)'
    const summary = getDynamicPricingSummary(
      pricingModel({
        billing_mode: 'tiered_expr',
        billing_expr: `(${base}) * (header("tier") == "fast" ? 2 : 1)`,
      }),
      { tokenUnit: 'M', now: new Date('2026-09-07T10:00:00Z') }
    )
    expect(summary?.tier?.label).toBe('first')
    expect(summary?.primaryEntries.map((entry) => entry.value)).toEqual([4, 12])
    expect(summary?.hasRequestRules).toBe(true)
    expect(
      summary?.primaryEntries.every((entry) => !entry.formattedRange)
    ).toBe(true)
  })
  test('keeps the first token tier even when a later tier is cheaper', () => {
    const summary = getDynamicPricingSummary(
      pricingModel({
        billing_mode: 'tiered_expr',
        billing_expr:
          'len < 1000 ? tier("first", p * 4 + c * 12) : tier("cheaper", p * 2 + c * 6)',
      }),
      { tokenUnit: 'M' }
    )
    expect(summary?.tier?.label).toBe('first')
    expect(summary?.primaryEntries.map((entry) => entry.value)).toEqual([4, 12])
    expect(
      summary?.primaryEntries.every(
        (entry) => entry.formattedRange === undefined
      )
    ).toBe(true)
  })
  test('preserves a versioned parenthesized base price and its request rule', () => {
    const summary = getDynamicPricingSummary(
      pricingModel({
        billing_mode: 'tiered_expr',
        billing_expr:
          'v1:(tier("base", p * 2 + c * 8)) * (header("x-priority") == "high" ? 2 : 1)',
      }),
      { tokenUnit: 'M' }
    )
    expect(summary?.isSpecialExpression).toBe(false)
    expect(summary?.primaryEntries.map((entry) => entry.value)).toEqual([2, 8])
    expect(summary?.hasRequestRules).toBe(true)
  })
  test.each([
    'tier("custom", max(p * 2 + c * 8, 100))',
    'tier("base", p * 2 + c * 8) * 3',
    'param("premium") ? tier("pro", p * 4 + c * 16) : tier("base", p * 2 + c * 8)',
    'tier("overflow", p * 1e999 + c * 8)',
  ])('does not invent structured prices from %s', (expression) => {
    const summary = getDynamicPricingSummary(
      pricingModel({ billing_mode: 'tiered_expr', billing_expr: expression }),
      { tokenUnit: 'M' }
    )
    expect(summary?.isSpecialExpression).toBe(true)
    expect(summary?.entries).toEqual([])
  })

  test('retains explicit zero token rates while omitting absent categories', () => {
    const summary = getDynamicPricingSummary(
      pricingModel({
        billing_mode: 'tiered_expr',
        billing_expr: 'tier("free", p * 0 + c * 0)',
      }),
      { tokenUnit: 'M' }
    )
    expect(
      summary?.primaryEntries.map((entry) => [entry.field, entry.value])
    ).toEqual([
      ['inputPrice', 0],
      ['outputPrice', 0],
    ])
    expect(summary?.secondaryEntries).toEqual([])
  })

  test('includes a free task tier in its range', () => {
    const summary = getDynamicPricingSummary(
      pricingModel({
        billing_mode: 'tiered_expr',
        billing_expr:
          'u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("free", u("seconds") * 0)',
        billing_usage_schema: {
          seconds: { type: 'number', unit: 'second' },
          mode: { enum: ['free', 'pro'] },
        },
      }),
      { tokenUnit: 'M' }
    )
    expect(summary?.primaryEntries[0]?.value).toBe(0)
    expect(summary?.primaryEntries[0]?.formattedRange).toBe('$0 – $0.8')
  })
})

describe('task dynamic pricing', () => {
  test('treats task coefficients as dollars per unit without a token divisor', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr:
        'u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)',
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
        mode: { enum: ['std', 'pro'] },
      },
    })

    const summary = getDynamicPricingSummary(model, summaryOptions)

    assert.ok(summary)
    assert.equal(summary.isTaskUsage, true)
    assert.equal(summary.isSpecialExpression, false)
    assert.equal(summary.tier?.label, 'std')
    assert.equal(summary.primaryEntries[0]?.value, 0.4)
    assert.equal(summary.primaryEntries[0]?.unit, 'second')
    assert.match(summary.primaryEntries[0]?.formatted ?? '', /0[.,]4/)
  })

  test('falls back for a non-canonical task expression', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr:
        'u("seconds") > 30 ? tier("long", u("seconds") * 0.3) : tier("short", u("seconds") * 0.4)',
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
      },
    })

    const summary = getDynamicPricingSummary(model, summaryOptions)

    assert.ok(summary)
    assert.equal(summary.isSpecialExpression, true)
    assert.equal(summary.tiers.length, 0)
  })

  test('summarizes different task tier prices as a range while preserving the fallback price', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr:
        'u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)',
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
        mode: { enum: ['std', 'pro'] },
      },
    })

    const summary = getDynamicPricingSummary(model, summaryOptions)

    assert.ok(summary)
    assert.match(summary.primaryEntries[0]?.formattedRange ?? '', /0[.,]4/)
    assert.match(summary.primaryEntries[0]?.formattedRange ?? '', /0[.,]8/)
    assert.match(summary.primaryEntries[0]?.formattedRange ?? '', /\S – \S/)
    assert.match(summary.primaryEntries[0]?.formatted ?? '', /0[.,]4/)
  })

  test('omits a task price range when every tier has the same unit price', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", u("seconds") * 0.4)',
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
      },
    })

    const summary = getDynamicPricingSummary(model, summaryOptions)

    assert.ok(summary)
    assert.equal(summary.primaryEntries[0]?.formattedRange, undefined)
  })

  test('identifies unconfigured task usage models without inventing token pricing', () => {
    const secondsModel = pricingModel({
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
      },
    })
    const countModel = pricingModel({
      billing_usage_schema: {
        clips: { type: 'number', unit: 'count' },
      },
    })

    assert.equal(hasTaskUsageSchema(secondsModel), true)
    assert.equal(isUnconfiguredTaskUsageModel(secondsModel), true)
    assert.equal(getDynamicPricingSummary(secondsModel, summaryOptions), null)
    assert.equal(getBillingModeLabelKey(secondsModel), 'Task billing')
    assert.equal(getBillingModeLabelKey(countModel), 'Task billing')
  })

  test('does not mark configured task usage pricing as unconfigured', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", u("seconds") * 0.4)',
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
      },
    })

    assert.equal(isUnconfiguredTaskUsageModel(model), false)
    assert.ok(getDynamicPricingSummary(model, summaryOptions))
  })

  test('leaves fixed per-request pricing configured when a usage schema is present', () => {
    const model = pricingModel({
      quota_type: 1,
      model_price: 0.5,
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
      },
    })

    assert.equal(isUnconfiguredTaskUsageModel(model), false)
    assert.equal(getDynamicPricingSummary(model, summaryOptions), null)
    assert.equal(isTokenBasedModel(model), false)
  })

  test('labels task token usage prices without changing chat token units', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", u("tokens") * 9.8 / 1000000)',
      billing_usage_schema: {
        tokens: { type: 'number', unit: 'token' },
      },
    })

    const summary = getDynamicPricingSummary(model, summaryOptions)

    assert.ok(summary)
    const tokenEntry = summary.primaryEntries[0]
    assert.ok(tokenEntry)
    assert.equal(tokenEntry.unit, 'token')
    assert.equal(tokenEntry.value, 9.8)
    assert.equal(getDynamicPriceUnitLabelKey(tokenEntry), '1M token')
    assert.equal(getTaskUsagePriceUnitLabelKey('token'), '1M token')
    assert.equal(
      getDynamicPriceUnitLabelKey({
        key: 'p',
        field: 'inputPrice',
        label: 'Input',
        shortLabel: 'Input',
        labelKind: 'i18n',
        value: 2,
        formatted: '$2',
        unit: 'token',
        variable: {
          key: 'p',
          field: 'inputPrice',
          tierField: 'input_unit_cost',
          label: 'Input price',
          shortLabel: 'Input',
          side: 'input',
        },
      }),
      null
    )
  })

  test('labels task credit usage prices as a direct per-credit rate', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", u("units") * 0.14)',
      billing_usage_schema: {
        units: { type: 'number', unit: 'credit' },
      },
    })

    const summary = getDynamicPricingSummary(model, summaryOptions)

    assert.ok(summary)
    const creditEntry = summary.primaryEntries[0]
    assert.ok(creditEntry)
    assert.equal(creditEntry.unit, 'credit')
    assert.equal(creditEntry.value, 0.14)
    assert.equal(getDynamicPriceUnitLabelKey(creditEntry), 'credit')
    assert.equal(getTaskUsagePriceUnitLabelKey('credit'), 'credit')
  })

  test('leaves token models without a usage schema unchanged', () => {
    const model = pricingModel({})

    assert.equal(hasTaskUsageSchema(model), false)
    assert.equal(isUnconfiguredTaskUsageModel(model), false)
    assert.equal(getBillingModeLabelKey(model), 'Token-based')
  })

  test('preserves all billing-mode badge states', () => {
    assert.equal(
      getBillingModeLabelKey(
        pricingModel({
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("base", u("seconds") * 0.4)',
          billing_usage_schema: {
            seconds: { type: 'number', unit: 'second' },
          },
        })
      ),
      'Task billing'
    )
    assert.equal(
      getBillingModeLabelKey(
        pricingModel({
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("base", u("clips") * 0.05)',
          billing_usage_schema: {
            clips: { type: 'number', unit: 'count' },
          },
        })
      ),
      'Task billing'
    )
    assert.equal(
      getBillingModeLabelKey(
        pricingModel({
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("base", u("tokens") * 9.8 / 1000000)',
          billing_usage_schema: {
            tokens: { type: 'number', unit: 'token' },
          },
        })
      ),
      'Task billing'
    )
    assert.equal(
      getBillingModeLabelKey(
        pricingModel({
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("base", u("units") * 0.14)',
          billing_usage_schema: {
            units: { type: 'number', unit: 'credit' },
          },
        })
      ),
      'Task billing'
    )
    assert.equal(
      getBillingModeLabelKey(
        pricingModel({
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("base", p * 2 + c * 8)',
        })
      ),
      'Dynamic Pricing'
    )
    assert.equal(getBillingModeLabelKey(pricingModel({})), 'Token-based')
    assert.equal(
      getBillingModeLabelKey(pricingModel({ quota_type: 1 })),
      'Per Request'
    )
  })

  test('marks task usage field labels as schema-owned so they are not translated', () => {
    const tokenModel = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", 0.1 + u("tokens") * 9.8 / 1000000)',
      billing_usage_schema: {
        tokens: { type: 'number', unit: 'token' },
      },
    })
    const tokenSummary = getDynamicPricingSummary(tokenModel, summaryOptions)
    assert.ok(tokenSummary)
    assert.equal(tokenSummary.primaryEntries[0]?.shortLabel, 'tokens')
    assert.equal(tokenSummary.primaryEntries[0]?.labelKind, 'schema')
    assert.equal(
      tokenSummary.secondaryEntries[0]?.shortLabel,
      'Additional charge'
    )
    assert.equal(tokenSummary.secondaryEntries[0]?.labelKind, 'i18n')

    const multiFieldModel = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr:
        'tier("base", u("seconds") * 0.4 + u("tokens") * 9.8 / 1000000)',
      billing_usage_schema: {
        seconds: { type: 'number', unit: 'second' },
        tokens: { type: 'number', unit: 'token' },
      },
    })
    const multiSummary = getDynamicPricingSummary(
      multiFieldModel,
      summaryOptions
    )
    assert.ok(multiSummary)
    assert.equal(multiSummary.primaryEntries.length, 2)
    assert.ok(
      multiSummary.primaryEntries.every((entry) => entry.labelKind === 'schema')
    )

    const chatSummary = getDynamicPricingSummary(
      pricingModel({
        billing_mode: 'tiered_expr',
        billing_expr: 'tier("base", p * 2 + c * 8)',
      }),
      summaryOptions
    )
    assert.ok(chatSummary)
    assert.ok(
      chatSummary.primaryEntries.every((entry) => entry.labelKind === 'i18n')
    )
  })

  test('returns the first evaluated usage example for a canonical task expression', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", u("tokens") * 9.8 / 1000000)',
      billing_usage_schema: {
        tokens: { type: 'number', unit: 'token' },
      },
      billing_usage_examples: [
        { label: '720p · 5s', facts: { tokens: 108000 } },
        { label: '1080p · 5s', facts: { tokens: 243000 } },
      ],
    })

    const example = getCardExamplePrice(model, summaryOptions)

    assert.ok(example)
    assert.equal(example.label, '720p · 5s')
    assert.match(example.formatted, /1[.,]0584/)
  })

  test('returns null when the expression is not canonical or examples are missing', () => {
    const schema = {
      tokens: { type: 'number' as const, unit: 'token' as const },
    }
    const examples = [{ label: '720p · 5s', facts: { tokens: 108000 } }]

    assert.equal(
      getCardExamplePrice(
        pricingModel({
          billing_mode: 'tiered_expr',
          billing_expr:
            'u("tokens") * 0.00007 * (u("tokens") > 100000 ? 0.8 : 1)',
          billing_usage_schema: schema,
          billing_usage_examples: examples,
        }),
        summaryOptions
      ),
      null
    )
    assert.equal(
      getCardExamplePrice(
        pricingModel({
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("base", u("tokens") * 9.8 / 1000000)',
          billing_usage_schema: schema,
        }),
        summaryOptions
      ),
      null
    )
    assert.equal(getCardExamplePrice(pricingModel({}), summaryOptions), null)
  })
})

describe('shared plugin price summaries', () => {
  test('uses a single remaining provider override instead of the model price', () => {
    const model = pricingModel({
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("default", u("seconds") * 0.1)',
      billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
      billing_plugin_variants: [
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          billing_expr: 'tier("provider", u("seconds") * 0.8)',
          billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
        },
      ],
    })
    expect(
      getDynamicPricingSummary(model, summaryOptions)?.primaryEntries[0].value
    ).toBe(0.8)
    expect(getDynamicPricingTiers(model).map((tier) => tier.label)).toEqual([
      'provider',
    ])
  })

  test('keeps per-call providers priced alongside an expression override', () => {
    const model = pricingModel({
      quota_type: 1,
      model_price: 0.25,
      billing_plugin_variants: [
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          billing_mode: 'ratio',
          billing_expr: '',
          billing_usage_schema: { images: { type: 'number', unit: 'count' } },
        },
        {
          plugin_key: 'beta',
          plugin_name: 'Beta',
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("provider", u("images") * 0.8)',
          billing_usage_schema: { images: { type: 'number', unit: 'count' } },
        },
      ],
    })
    const summary = getDynamicPricingSummary(model, summaryOptions)
    expect(summary?.hasUnconfiguredProviders).toBe(false)
    expect(summary?.primaryEntries).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ unit: 'request', value: 0.25 }),
        expect.objectContaining({ unit: 'count', value: 0.8 }),
      ])
    )
    expect(
      isUnconfiguredTaskUsageModel({
        ...model,
        billing_plugin_variants: model.billing_plugin_variants?.slice(0, 1),
      })
    ).toBe(false)
  })

  test('retains a later provider’s display unit when the first has no label', () => {
    const model = pricingModel({
      billing_plugin_variants: [
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          billing_expr: 'tier("base", u("images") * 0.4)',
          billing_usage_schema: { images: { type: 'number', unit: 'count' } },
        },
        {
          plugin_key: 'beta',
          plugin_name: 'Beta',
          billing_expr: 'tier("base", u("images") * 0.8)',
          billing_usage_schema: {
            images: {
              type: 'number',
              unit: 'count',
              unitLabel: { en: 'image', zh: '张' },
            },
          },
        },
      ],
    })
    expect(
      getDynamicPricingSummary(model, summaryOptions)?.primaryEntries[0]
    ).toMatchObject({
      unitLabel: { en: 'image', zh: '张' },
      minValue: 0.4,
      maxValue: 0.8,
    })
  })

  test('merges each provider’s tier ranges without changing task-unit conversion', () => {
    const model = pricingModel({
      billing_plugin_variants: [
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          billing_expr:
            'u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("base", u("seconds") * 0.4)',
          billing_usage_schema: {
            seconds: { type: 'number', unit: 'second' },
            mode: { enum: ['pro', 'base'] },
          },
        },
        {
          plugin_key: 'beta',
          plugin_name: 'Beta',
          billing_expr: 'tier("base", u("seconds") * 1.2)',
          billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
        },
      ],
    })
    const summary = getDynamicPricingSummary(model, summaryOptions)
    expect(summary?.providerCount).toBe(2)
    expect(summary?.tierCount).toBe(3)
    expect(summary?.hasUnconfiguredProviders).toBe(false)
    expect(summary?.primaryEntries).toHaveLength(1)
    expect(summary?.primaryEntries[0]).toMatchObject({
      field: 'seconds',
      minValue: 0.4,
      maxValue: 1.2,
      formattedRange: '$0.4 – $1.2',
      unit: 'second',
    })
  })

  test('retains separate field names and units and flags unconfigured providers', () => {
    const model = pricingModel({
      billing_plugin_variants: [
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          billing_expr: 'tier("base", u("quantity") * 1)',
          billing_usage_schema: {
            quantity: { type: 'number', unit: 'second' },
          },
        },
        {
          plugin_key: 'beta',
          plugin_name: 'Beta',
          billing_expr: 'tier("base", u("quantity") * 2)',
          billing_usage_schema: {
            quantity: { type: 'number', unit: 'credit' },
          },
        },
        {
          plugin_key: 'gamma',
          plugin_name: 'Gamma',
          billing_expr: '',
          billing_usage_schema: { images: { type: 'number', unit: 'count' } },
        },
      ],
    })
    const summary = getDynamicPricingSummary(model, summaryOptions)
    expect(summary?.primaryEntries.map((entry) => entry.unit)).toEqual([
      'second',
      'credit',
    ])
    expect(
      summary?.primaryEntries.every((entry) => !entry.formattedRange)
    ).toBe(true)
    expect(summary?.hasUnconfiguredProviders).toBe(true)
    expect(isUnconfiguredTaskUsageModel(model)).toBe(false)
    const empty = {
      ...model,
      billing_plugin_variants: model.billing_plugin_variants?.map(
        (variant) => ({ ...variant, billing_expr: '' })
      ),
    }
    expect(isUnconfiguredTaskUsageModel(empty)).toBe(true)
    expect(
      getDynamicPricingSummary(empty, summaryOptions)?.primaryEntries
    ).toEqual([])
  })

  test('shows each provider’s first numeric field and chooses the first priced example', () => {
    const model = pricingModel({
      billing_plugin_variants: [
        {
          plugin_key: 'alpha',
          plugin_name: 'Alpha',
          billing_expr: '',
          billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
        },
        {
          plugin_key: 'beta',
          plugin_name: 'Beta',
          billing_expr: 'tier("base", u("credits") * 0.5 + u("extras") * 2)',
          billing_usage_schema: {
            credits: { type: 'number', unit: 'credit' },
            extras: { type: 'number', unit: 'count' },
          },
          billing_usage_examples: [
            { label: 'Beta sample', facts: { credits: 2, extras: 1 } },
          ],
        },
        {
          plugin_key: 'gamma',
          plugin_name: 'Gamma',
          billing_expr: 'tier("base", u("images") * 0.3)',
          billing_usage_schema: { images: { type: 'number', unit: 'count' } },
        },
      ],
    })
    expect(
      getDynamicPricingSummary(model, summaryOptions)
        ?.primaryEntries.slice(0, 2)
        .map((entry) => entry.field)
    ).toEqual(['credits', 'images'])
    expect(getCardExamplePrice(model, summaryOptions)).toEqual({
      label: 'Beta sample',
      formatted: '$3',
    })
  })
})
