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

import { parseTaskTiersFromExpr } from '../lib/billing-expr'
import { evaluateBillingExpression } from '../lib/billing-expression/runtime'
import { getDynamicPriceEntries } from '../lib/dynamic-price'
import {
  getTaskMatrixDisplayTiers,
  getTaskPricingDisplayTiers,
} from '../lib/task-matrix-display'
import type { BillingUsageSchema } from '../types'

const resolutionSchema: BillingUsageSchema = {
  seconds: { type: 'number', unit: 'second' },
  resolution: { enum: ['480P', '720P', '1080P'] },
}

const doubleEnumSchema: BillingUsageSchema = {
  quality: { enum: ['high', 'low'] },
  seconds: { type: 'number', unit: 'second' },
  mode: { enum: ['std', 'pro'] },
}

const numberOnlySchema: BillingUsageSchema = {
  seconds: { type: 'number', unit: 'second' },
}

describe('conditional task price display', () => {
  const schema: BillingUsageSchema = {
    seconds: { type: 'number', unit: 'second' },
    resolution: { enum: ['768P', '1080P', '2K', '4K'] },
  }
  const expression =
    'tier("standard", u("seconds") * (hour("Asia/Shanghai") >= 18 && hour("Asia/Shanghai") < 22 ? (u("resolution") == "1080P" || u("resolution") == "2K" ? 0.096 : u("resolution") == "4K" ? 0.12 : 0.072) : (u("resolution") == "1080P" || u("resolution") == "2K" ? 0.12 : u("resolution") == "4K" ? 0.15 : 0.09)))'

  test('expands nested time and resolution prices without changing the editable expression contract', () => {
    const tiers = getTaskPricingDisplayTiers(expression, schema)
    expect(tiers).toHaveLength(8)
    expect(tiers.map((tier) => tier.unitPrices.seconds)).toEqual([
      0.072, 0.09, 0.096, 0.12, 0.096, 0.12, 0.12, 0.15,
    ])
    expect(parseTaskTiersFromExpr(expression, schema)).toEqual([])
    for (const clock of ['17:59:00', '18:00:00', '21:59:00', '22:00:00']) {
      const now = new Date(`2026-09-18T${clock}+08:00`)
      for (const resolution of ['768P', '1080P', '2K', '4K']) {
        const usage = { seconds: 10, resolution }
        const matches = tiers.filter((tier) => {
          if (
            !tier.conditions.every(
              (condition) => condition.value === resolution
            )
          ) {
            return false
          }
          const result = evaluateBillingExpression(
            `(${tier.conditionText}) ? 1 : 0`,
            { now }
          )
          return result.status === 'success' && result.cost === 1
        })
        expect(matches).toHaveLength(1)
        const actual = evaluateBillingExpression(expression, { usage, now })
        expect(actual.status).toBe('success')
        if (actual.status === 'success') {
          expect(matches[0].unitPrices.seconds * 10).toBeCloseTo(
            actual.cost,
            12
          )
        }
      }
    }
  })

  test('supports reversed multiplication, enum OR and token scaling while preserving zero prices', () => {
    const tiers = getTaskPricingDisplayTiers(
      'tier("base", 0.1 + (u("resolution") == "1080P" || u("resolution") == "2K" ? 9.8 : 0) * u("tokens") / 1000000)',
      { ...schema, tokens: { type: 'number', unit: 'token' } }
    )
    expect(tiers.map((tier) => tier.unitPrices.tokens)).toEqual([
      0, 9.8, 9.8, 0,
    ])
    expect(tiers.every((tier) => tier.constant === 0.1)).toBe(true)
  })

  test.each([
    'tier("base", u("seconds") * u("seconds"))',
    'tier("base", u("seconds") / u("seconds"))',
    'tier("base", u("seconds") * (param("resolution") == "4K" ? 0.15 : 0.09))',
    'tier("base", u("seconds") * (u("seconds") > 30 ? 0.15 : 0.09))',
    'tier("base", u("seconds") * (u("missing") == "4K" ? 0.15 : 0.09))',
    'tier("base", u("seconds") * (u("resolution") == "4K" ? 0.15 : 1 / 0))',
    'tier("base", u("seconds") * (u("resolution") == "4K" ? 0.15 : -0.09))',
    'tier("base", u("seconds") * (hour("UTC") > "18" ? 0.15 : 0.09))',
  ])('keeps the entire unsupported expression as a fallback: %s', (source) => {
    expect(getTaskPricingDisplayTiers(source, schema)).toEqual([])
  })

  test('keeps enum defaults, boolean facts, constants and multiple usage units exact', () => {
    const rows = getTaskPricingDisplayTiers(
      'u("audio") == true && u("resolution") != "4K" ? tier("base", 0.1 + (u("seconds") * 0.2 + u("clips") * 0.5) * 2) : tier("base", u("seconds") * 0)',
      {
        ...schema,
        audio: { type: 'boolean' },
        clips: { type: 'number', unit: 'count' },
      }
    )
    expect(rows).toHaveLength(8)
    const paid = rows.filter((row) => row.constant === 0.1)
    expect(paid).toHaveLength(3)
    expect(
      paid.every(
        (row) => row.unitPrices.seconds === 0.4 && row.unitPrices.clips === 1
      )
    ).toBe(true)
    expect(
      rows
        .filter((row) => row.constant === 0)
        .every(
          (row) => row.unitPrices.seconds === 0 && row.unitPrices.clips === 0
        )
    ).toBe(true)
  })

  test('treats a nested condition on an undeclared enum value as never matching', () => {
    const tiers = getTaskPricingDisplayTiers(expression, {
      ...schema,
      resolution: { enum: ['768P', '1080P'] },
    })
    expect(tiers).toHaveLength(4)
    expect(tiers.map((tier) => tier.unitPrices.seconds)).toEqual([
      0.072, 0.09, 0.096, 0.12,
    ])
  })

  test('falls back completely when categorical expansion exceeds the display limit', () => {
    expect(
      getTaskPricingDisplayTiers(
        'tier("base", u("seconds") * (u("resolution") == "0" ? 0.1 : 0.2))',
        {
          ...schema,
          resolution: {
            enum: Array.from({ length: 129 }, (_, index) => String(index)),
          },
        }
      )
    ).toEqual([])
  })
})

describe('task matrix marketplace display rows', () => {
  test('expands a uniform flat expression into every enum combination', () => {
    const rows = getTaskMatrixDisplayTiers(
      'tier("base", u("seconds") * 0.4)',
      resolutionSchema
    )

    assert.deepEqual(rows, [
      {
        label: '480P',
        conditions: [{ field: 'resolution', value: '480P' }],
        constant: 0,
        unitPrices: { seconds: 0.4 },
      },
      {
        label: '720P',
        conditions: [{ field: 'resolution', value: '720P' }],
        constant: 0,
        unitPrices: { seconds: 0.4 },
      },
      {
        label: '1080P',
        conditions: [{ field: 'resolution', value: '1080P' }],
        constant: 0,
        unitPrices: { seconds: 0.4 },
      },
    ])
  })

  test('expands a full non-uniform partition in canonical order with combination labels', () => {
    const expression =
      'u("mode") == "std" && u("quality") == "high" ? tier("std·high", 0.1 + u("seconds") * 0.2) : u("mode") == "std" && u("quality") == "low" ? tier("std·low", 0.2 + u("seconds") * 0.3) : u("mode") == "pro" && u("quality") == "high" ? tier("pro·high", 0.3 + u("seconds") * 0.4) : tier("pro·low", 0.4 + u("seconds") * 0.5)'

    assert.deepEqual(getTaskMatrixDisplayTiers(expression, doubleEnumSchema), [
      {
        label: 'std·high',
        conditions: [
          { field: 'mode', value: 'std' },
          { field: 'quality', value: 'high' },
        ],
        constant: 0.1,
        unitPrices: { seconds: 0.2 },
      },
      {
        label: 'std·low',
        conditions: [
          { field: 'mode', value: 'std' },
          { field: 'quality', value: 'low' },
        ],
        constant: 0.2,
        unitPrices: { seconds: 0.3 },
      },
      {
        label: 'pro·high',
        conditions: [
          { field: 'mode', value: 'pro' },
          { field: 'quality', value: 'high' },
        ],
        constant: 0.3,
        unitPrices: { seconds: 0.4 },
      },
      {
        label: 'pro·low',
        conditions: [
          { field: 'mode', value: 'pro' },
          { field: 'quality', value: 'low' },
        ],
        constant: 0.4,
        unitPrices: { seconds: 0.5 },
      },
    ])
  })

  test('returns null for a number-only schema so the single-row display stays', () => {
    assert.equal(
      getTaskMatrixDisplayTiers(
        'tier("base", u("seconds") * 0.4)',
        numberOnlySchema
      ),
      null
    )
  })

  test('returns null for an unrecognizable sparse expression', () => {
    assert.equal(
      getTaskMatrixDisplayTiers(
        'u("seconds") > 30 ? tier("long", u("seconds") * 0.3) : tier("short", u("seconds") * 0.4)',
        resolutionSchema
      ),
      null
    )
  })

  test('returns null when there is no usage schema', () => {
    assert.equal(
      getTaskMatrixDisplayTiers('tier("base", p * 2 + c * 8)', undefined),
      null
    )
  })

  test('keeps group-ratio multiplication on expanded display-row unit prices', () => {
    const rows = getTaskMatrixDisplayTiers(
      'tier("base", 0.1 + u("seconds") * 0.4)',
      resolutionSchema
    )
    assert.ok(rows)
    assert.equal(rows.length, 3)

    const baseEntries = getDynamicPriceEntries(rows[0], {
      tokenUnit: 'K',
      showRechargePrice: false,
      usageSchema: resolutionSchema,
      groupRatioMultiplier: 1,
    })
    const doubledEntries = getDynamicPriceEntries(rows[0], {
      tokenUnit: 'K',
      showRechargePrice: false,
      usageSchema: resolutionSchema,
      groupRatioMultiplier: 2,
    })

    assert.equal(baseEntries[0]?.value, 0.4)
    assert.equal(doubledEntries[0]?.value, 0.4)
    assert.match(baseEntries[0]?.formatted ?? '', /0[.,]4/)
    assert.match(doubledEntries[0]?.formatted ?? '', /0[.,]8/)
    assert.equal(baseEntries.at(-1)?.value, 0.1)
    assert.match(doubledEntries.at(-1)?.formatted ?? '', /0[.,]2/)
  })
})
