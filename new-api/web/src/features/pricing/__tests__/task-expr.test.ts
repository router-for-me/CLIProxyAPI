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

import { describe, test } from 'vitest'

import {
  combineBillingExpr,
  parseTaskTiersFromExpr,
  splitBillingExprAndRequestRules,
} from '../lib/billing-expr'
import {
  evaluateTaskUsageExamples,
  evaluateTaskVisualConfig,
  generateTaskExprFromConfig,
  tryParseTaskVisualConfig,
  type TaskVisualConfig,
} from '../lib/task-expr'
import type { BillingUsageSchema } from '../types'

const schema: BillingUsageSchema = {
  seconds: { type: 'number', unit: 'second' },
  clips: { type: 'number', unit: 'count' },
  mode: { enum: ['std', 'pro'] },
}

function assertConfigRoundTrip(config: TaskVisualConfig) {
  const expression = generateTaskExprFromConfig(config, schema)
  const parsed = tryParseTaskVisualConfig(expression, schema)
  assert.ok(parsed)
  assert.equal(generateTaskExprFromConfig(parsed, schema), expression)
}

describe('task billing expressions', () => {
  test('round-trips flat, enum-tiered, and additive canonical shapes', () => {
    assertConfigRoundTrip({
      tiers: [
        {
          label: 'base',
          conditions: [],
          constant: 0,
          unitPrices: { seconds: 0.4, clips: 0 },
        },
      ],
    })
    assertConfigRoundTrip({
      tiers: [
        {
          label: 'pro',
          conditions: [{ field: 'mode', value: 'pro' }],
          constant: 0,
          unitPrices: { seconds: 0.8, clips: 0 },
        },
        {
          label: 'std',
          conditions: [],
          constant: 0,
          unitPrices: { seconds: 0.4, clips: 0 },
        },
      ],
    })
    assertConfigRoundTrip({
      tiers: [
        {
          label: 'base',
          conditions: [],
          constant: 0.1,
          unitPrices: { seconds: 0.4, clips: 0.05 },
        },
      ],
    })
  })

  test('preserves request-rule factors around a canonical task expression', () => {
    const baseExpression = generateTaskExprFromConfig(
      {
        tiers: [
          {
            label: 'base',
            conditions: [],
            constant: 0,
            unitPrices: { seconds: 0.4, clips: 0 },
          },
        ],
      },
      schema
    )
    const requestRules = '(header("x-priority") == "high" ? 2 : 1)'
    const combined = combineBillingExpr(baseExpression, requestRules)
    const split = splitBillingExprAndRequestRules(combined)

    assert.equal(split.requestRuleExpr, requestRules)
    const parsed = tryParseTaskVisualConfig(split.billingExpr, schema)
    assert.ok(parsed)
    assert.equal(
      combineBillingExpr(
        generateTaskExprFromConfig(parsed, schema),
        requestRules
      ),
      combined
    )
  })

  test('rejects expressions outside the frozen task shapes', () => {
    assert.equal(tryParseTaskVisualConfig('u("seconds") * 0.4', schema), null)
    assert.equal(
      tryParseTaskVisualConfig(
        'u("seconds") > 30 ? tier("long", u("seconds") * 0.3) : tier("short", u("seconds") * 0.4)',
        schema
      ),
      null
    )
    assert.equal(
      tryParseTaskVisualConfig('tier("base", u("unknown") * 0.4)', schema),
      null
    )
  })

  test('drops tiers on enum values the schema no longer declares', () => {
    const expression =
      'u("mode") == "ultra" ? tier("ultra", u("seconds") * 1.2) : u("mode") == "pro" ? tier("pro", u("seconds") * 0.8) : tier("std", u("seconds") * 0.4)'
    const parsed = tryParseTaskVisualConfig(expression, schema)
    assert.ok(parsed)
    assert.deepEqual(
      parsed.tiers.map((tier) => [tier.label, tier.unitPrices.seconds]),
      [
        ['pro', 0.8],
        ['std', 0.4],
      ]
    )
    assert.deepEqual(
      parseTaskTiersFromExpr(expression, schema).map((tier) => tier.label),
      ['pro', 'std']
    )
  })
})

describe('task visual pricing preview', () => {
  test('totals a base charge and usage price for a single tier', () => {
    const tier = {
      label: 'base',
      conditions: [],
      constant: 0.02,
      unitPrices: { seconds: 0.1 },
    }

    const result = evaluateTaskVisualConfig({ tiers: [tier] }, { seconds: 5 })

    assert.ok(result)
    assert.equal(result.tier, tier)
    assert.equal(result.total, 0.52)
    assert.deepEqual(result.parts, [
      { kind: 'constant', amount: 0.02 },
      {
        kind: 'usage',
        field: 'seconds',
        amount: 0.5,
        quantity: 5,
        unitPrice: 0.1,
      },
    ])
  })

  test('matches enum tiers in order and otherwise uses the fallback', () => {
    const config: TaskVisualConfig = {
      tiers: [
        {
          label: 'pro',
          conditions: [{ field: 'mode', value: 'pro' }],
          constant: 0,
          unitPrices: { seconds: 0.8 },
        },
        {
          label: 'std',
          conditions: [],
          constant: 0,
          unitPrices: { seconds: 0.4 },
        },
      ],
    }

    assert.equal(
      evaluateTaskVisualConfig(config, { mode: 'pro', seconds: 1 })?.tier.label,
      'pro'
    )
    assert.equal(
      evaluateTaskVisualConfig(config, { mode: 'std', seconds: 1 })?.tier.label,
      'std'
    )
    assert.equal(
      evaluateTaskVisualConfig(config, { mode: 'unknown', seconds: 1 })?.tier
        .label,
      'std'
    )
  })

  test('requires every enum condition on a multi-condition tier', () => {
    const config: TaskVisualConfig = {
      tiers: [
        {
          label: 'extend-two',
          conditions: [
            { field: 'action', value: 'extend' },
            { field: 'quality', value: 'high' },
          ],
          constant: 0,
          unitPrices: { clips: 0.2 },
        },
        {
          label: 'base',
          conditions: [],
          constant: 0,
          unitPrices: { clips: 0.1 },
        },
      ],
    }

    assert.equal(
      evaluateTaskVisualConfig(config, {
        action: 'extend',
        quality: 'high',
        clips: 2,
      })?.tier.label,
      'extend-two'
    )
    assert.equal(
      evaluateTaskVisualConfig(config, {
        action: 'extend',
        quality: 'standard',
        clips: 2,
      })?.tier.label,
      'base'
    )
  })

  test('selects the same tier after round-tripping through the expression grammar', () => {
    const config: TaskVisualConfig = {
      tiers: [
        {
          label: 'pro',
          conditions: [{ field: 'mode', value: 'pro' }],
          constant: 0.05,
          unitPrices: { seconds: 0.8, clips: 0.1 },
        },
        {
          label: 'std',
          conditions: [],
          constant: 0.02,
          unitPrices: { seconds: 0.4, clips: 0.05 },
        },
      ],
    }
    const sample = { mode: 'pro', seconds: 5, clips: 2 }
    const expression = generateTaskExprFromConfig(config, schema)
    const parsedTiers = parseTaskTiersFromExpr(expression, schema)
    assert.ok(parsedTiers.length > 0)
    const grammarTier =
      parsedTiers
        .slice(0, -1)
        .find((tier) =>
          tier.conditions.every(
            (condition) =>
              sample[condition.field as keyof typeof sample] === condition.value
          )
        ) ?? parsedTiers.at(-1)
    assert.ok(grammarTier)

    const result = evaluateTaskVisualConfig(config, sample)

    assert.ok(result)
    assert.equal(result.tier.label, grammarTier.label)
  })

  test('round-trips a token field at the $/1M editor scale', () => {
    const tokenSchema: BillingUsageSchema = {
      tokens: { type: 'number', unit: 'token' },
    }
    const config: TaskVisualConfig = {
      tiers: [
        {
          label: 'base',
          conditions: [],
          constant: 0,
          unitPrices: { tokens: 9.8 },
        },
      ],
    }

    const expression = generateTaskExprFromConfig(config, tokenSchema)
    assert.match(expression, /\/ 1000000/)
    assert.equal(expression, 'tier("base", u("tokens") * 9.8 / 1000000)')

    const parsed = tryParseTaskVisualConfig(expression, tokenSchema)
    assert.ok(parsed)
    assert.equal(parsed.tiers[0].unitPrices.tokens, 9.8)
    assert.equal(generateTaskExprFromConfig(parsed, tokenSchema), expression)
  })

  test('treats a bare token term as unparseable so old $/token expressions stay raw', () => {
    const tokenSchema: BillingUsageSchema = {
      tokens: { type: 'number', unit: 'token' },
    }
    assert.equal(
      tryParseTaskVisualConfig(
        'tier("base", u("tokens") * 0.0000098)',
        tokenSchema
      ),
      null
    )
    assert.deepEqual(
      parseTaskTiersFromExpr(
        'tier("base", u("tokens") * 0.0000098)',
        tokenSchema
      ),
      []
    )
  })

  test('round-trips a credit field without a /1M division', () => {
    const creditSchema: BillingUsageSchema = {
      units: { type: 'number', unit: 'credit' },
    }
    const config: TaskVisualConfig = {
      tiers: [
        {
          label: 'base',
          conditions: [],
          constant: 0,
          unitPrices: { units: 0.14 },
        },
      ],
    }

    const expression = generateTaskExprFromConfig(config, creditSchema)
    assert.equal(expression, 'tier("base", u("units") * 0.14)')
    assert.doesNotMatch(expression, /\/ 1000000/)

    const parsed = tryParseTaskVisualConfig(expression, creditSchema)
    assert.ok(parsed)
    assert.equal(parsed.tiers[0].unitPrices.units, 0.14)
  })

  test('maps declared usage example labels to evaluated prices', () => {
    const tokenSchema: BillingUsageSchema = {
      tokens: { type: 'number', unit: 'token' },
    }
    const config = tryParseTaskVisualConfig(
      'tier("base", u("tokens") * 9.8 / 1000000)',
      tokenSchema
    )
    assert.ok(config)

    const result = evaluateTaskVisualConfig(
      config,
      { tokens: 108000 },
      tokenSchema
    )
    assert.ok(result)
    assert.equal(result.total, (108000 * 9.8) / 1_000_000)

    assert.deepEqual(
      evaluateTaskUsageExamples(
        'tier("base", u("tokens") * 9.8 / 1000000)',
        tokenSchema,
        [
          { label: '720p · 5s', facts: { tokens: 108000 } },
          { label: '1080p · 5s', facts: { tokens: 243000 } },
        ]
      ),
      [
        { label: '720p · 5s', total: (108000 * 9.8) / 1_000_000 },
        { label: '1080p · 5s', total: (243000 * 9.8) / 1_000_000 },
      ]
    )
  })

  test('returns no usage example prices for a raw unparseable expression', () => {
    assert.deepEqual(
      evaluateTaskUsageExamples(
        'u("tokens") * 0.00007 * (u("tokens") > 100000 ? 0.8 : 1)',
        { tokens: { type: 'number', unit: 'token' } },
        [{ label: '720p · 5s', facts: { tokens: 108000 } }]
      ),
      []
    )
  })
})
