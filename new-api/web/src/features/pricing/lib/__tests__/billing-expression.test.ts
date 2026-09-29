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
import { createInstance } from 'i18next'
import { assert, afterEach, describe, expect, test, vi } from 'vitest'

import { getTieredBillingSummary } from '@/features/usage-logs/lib/format'
import zh from '@/i18n/locales/zh.json'

import contract from '../../../../../../pkg/billingexpr/testdata/frontend_simulation.json'
import {
  combineBillingExpr,
  splitBillingExprAndRequestRules,
} from '../billing-expr'
import { formatBillingCondition } from '../billing-expression/condition-display'
import { readTokenTierChain } from '../billing-expression/display'
import { compileBillingExpression } from '../billing-expression/parser'
import {
  evaluateBillingExpression,
  timeInBillingZone,
} from '../billing-expression/runtime'
import type { TokenVariable } from '../billing-expression/types'
import {
  parseVisualBillingDocument,
  serializeVisualBillingDocument,
  visualNodeId,
  type VisualCondition,
} from '../billing-expression/visual'
import {
  buildEstimatorTokens,
  evalExprLocally,
  type ExtraTokenValues,
} from '../tier-expr'

const extras: ExtraTokenValues = {
  cacheReadTokens: 100,
  cacheCreateTokens: 0,
  cacheCreate1hTokens: 0,
  imageTokens: 0,
  imageOutputTokens: 0,
  imageCacheTokens: 0,
  audioInputTokens: 0,
  audioOutputTokens: 0,
}

test('evaluates and round-trips separate image cache prices including an explicit zero', () => {
  const source =
    'tier("standard", p * 5 + cr * 1.25 + img * 8 + img_cr * 2 + c * 30)'
  const document = parseVisualBillingDocument(source)
  expect(
    buildEstimatorTokens(300, 100, {
      ...extras,
      cacheReadTokens: 100,
      imageTokens: 400,
      imageCacheTokens: 200,
    }).len
  ).toBe(1000)
  assert(document)
  const regenerated = serializeVisualBillingDocument(document)
  assert(regenerated.ok)
  expect(
    evaluateBillingExpression(regenerated.source, {
      tokens: { p: 300, cr: 100, img: 400, img_cr: 200, c: 100, len: 1000 },
    })
  ).toMatchObject({ status: 'success', cost: 8225, matchedTier: 'standard' })
  expect(
    evalExprLocally(source, 300, 100, {
      ...extras,
      cacheReadTokens: 100,
      imageTokens: 400,
      imageCacheTokens: 200,
    })
  ).toMatchObject({ cost: 8225, error: null })
  const freeCache = parseVisualBillingDocument(
    source.replace('img_cr * 2', 'img_cr * 0')
  )
  assert(freeCache)
  const freeResult = serializeVisualBillingDocument(freeCache)
  assert(freeResult.ok)
  expect(freeResult.source).toContain('img_cr * 0')
})

export const peakCondition =
  'weekday("Asia/Shanghai") >= 1 && weekday("Asia/Shanghai") <= 5 && ((hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12) || (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18))'
export const deepSeekExpression = `${peakCondition} ? tier("peak", p * 3 + cr * 0.10 + c * 9) : tier("off_peak", p * 1.5 + cr * 0.05 + c * 4.5)`

afterEach(() => vi.useRealTimers())

describe('local billing expression evaluation', () => {
  test('evaluates request prices without token inputs and retains an explicit zero price', () => {
    expect(
      evaluateBillingExpression('tier("request", fixed(0.01))')
    ).toMatchObject({
      status: 'success',
      cost: 10000,
      billingUnit: 'request',
      fixedPrice: 0.01,
    })
    expect(evaluateBillingExpression('tier("free", fixed(0))')).toMatchObject({
      status: 'success',
      cost: 0,
      billingUnit: 'request',
      fixedPrice: 0,
    })
  })
  test('preserves mixed fixed and token leaves through an unedited visual round trip', () => {
    const source =
      'v1:len <= 32000 ? tier(\'short\', fixed(0.0100)) : tier("long", p * 2 + c * 8)'
    const document = parseVisualBillingDocument(source)
    assert(document)
    expect(serializeVisualBillingDocument(document)).toEqual({
      ok: true,
      source,
    })
  })
  test('localizes peak and complement conditions without exposing source code', async () => {
    const translations = createInstance()
    await translations.init({ lng: 'zh', resources: { zh } })
    expect(formatBillingCondition(peakCondition, translations.t, 'zh')).toBe(
      '周一至周五 09:00至12:00或14:00至18:00（Asia/Shanghai）'
    )
    expect(
      formatBillingCondition(`!(${peakCondition})`, translations.t, 'zh')
    ).toBe('周一至周五 09:00至12:00或14:00至18:00以外的时段（Asia/Shanghai）')
  })
  test.each([
    ['zhCN', 'weekday("UTC") == 1', '周一 (UTC)'],
    ['zhTW', 'weekday("UTC") == 1', '週一 (UTC)'],
    ['zhCN', 'len > 32000', 'Full input length > 32,000'],
    ['zhTW', 'len > 32000', 'Full input length > 32,000'],
  ])(
    'formats %s condition %s without falling back to source',
    async (language, source, expected) => {
      const translations = createInstance()
      await translations.init({
        lng: 'en',
        resources: { en: { translation: {} } },
      })
      expect(formatBillingCondition(source, translations.t, language)).toBe(
        expected
      )
    }
  )
  test('keeps log prices tied to the recorded tier regardless of the current time', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-07T10:00:00+08:00'))
    const log = {
      billing_mode: 'tiered_expr',
      expr_b64: btoa(deepSeekExpression),
      matched_tier: 'off_peak',
    }
    expect(getTieredBillingSummary(log)?.tier.inputPrice).toBe(1.5)
    expect(
      getTieredBillingSummary({ ...log, matched_tier: 'missing' })
    ).toBeNull()
  })

  test('preserves quoted operators while splitting and recombining request rules', () => {
    const base = 'tier("name ( * )", p * 2 + c * 8)'
    const rules = '(header("x-rule") == "a * b" ? 2 : 1)'
    expect(
      splitBillingExprAndRequestRules(combineBillingExpr(base, rules))
    ).toEqual({ billingExpr: base, requestRuleExpr: rules })
  })
  test.each(contract)('agrees with the Go engine: $name', (fixture) => {
    const result = evaluateBillingExpression(fixture.expression, {
      imageCount: fixture.imageCount,
      tokens: fixture.tokens as Partial<Record<TokenVariable, number>>,
      request: {
        body: fixture.body ?? {},
        headers: fixture.headers as Record<string, string> | undefined,
      },
      usage: fixture.usage,
    })
    expect(result.status).toBe('success')
    if (result.status !== 'success') return
    expect(result.cost).toBeCloseTo(fixture.cost, 9)
    expect(result.matchedTier).toBe(fixture.tier)
    if (fixture.billingUnit) {
      expect(result.billingUnit).toBe(fixture.billingUnit)
      expect(result.fixedPrice).toBe(fixture.fixedPrice)
    }
    expect(result.requestRules.map((rule) => rule.multiplier)).toEqual(
      fixture.multipliers ?? []
    )
    expect(result.requestRules.map((rule) => rule.matched)).toEqual(
      fixture.matched ?? []
    )
    if (fixture.displayTiers) {
      const compiled = compileBillingExpression(fixture.expression)
      assert(compiled.status === 'ready')
      expect(readTokenTierChain(compiled.ast)).toEqual(fixture.displayTiers)
    }
  })

  test('does not summarize unrelated nonlinear input expressions as audio prices', () => {
    const compiled = compileBillingExpression(
      'tier("audio", max(len - cr, 0) * 2 + ai * 4)'
    )
    assert(compiled.status === 'ready')
    expect(readTokenTierChain(compiled.ast)).toBeNull()
  })

  test('distinguishes unknown requests from explicitly empty requests', () => {
    const expression = 'param("priority") == nil ? 2 : 3'
    expect(evaluateBillingExpression(expression).status).toBe('missing_context')
    expect(
      evaluateBillingExpression(expression, { request: {} })
    ).toMatchObject({ status: 'success', cost: 2 })
  })

  test.each([
    ['globalThis.process', 'unsupported'],
    ['tier("bad", p * 1e999)', 'invalid'],
    ['v2:tier("base", 1)', 'unsupported'],
    ['tier("bad", -1)', 'invalid'],
    ['1 / 0', 'invalid'],
    ['tier("base", 1) +', 'invalid'],
    ['param("items.#(price>1)")', 'unsupported'],
    ['param("n") + 1', 'invalid'],
    ['param("n") % 2', 'invalid'],
    ['9007199254740993', 'unsupported'],
  ])(
    'reports %s as %s without a successful zero fallback',
    (expression, status) => {
      const result = evaluateBillingExpression(expression, {
        request: { body: { n: '2' } },
      })
      expect(result.status).toBe(status)
      expect(result).not.toHaveProperty('cost')
    }
  )

  test('recognizes real variable dependencies without scanning strings', () => {
    const result = compileBillingExpression('tier("cr weekday", p * 2 + c * 3)')
    expect(result.status).toBe('ready')
    if (result.status !== 'ready') return
    expect([...result.variables]).toEqual(['p', 'c'])
    expect([...result.functions]).toEqual(['tier'])
  })

  test('keeps legacy normalized tokens while accepting a full context length override', () => {
    const expression =
      'len < 2000 ? tier("short", p * 2 + cr * 0.1) : tier("long", p * 4 + cr * 0.2)'
    expect(evalExprLocally(expression, 1000, 0, extras)).toMatchObject({
      cost: 2010,
      matchedTier: 'short',
    })
    expect(
      evalExprLocally(expression, 1000, 0, extras, { tokens: { len: 2500 } })
    ).toMatchObject({ cost: 4020, matchedTier: 'long' })
  })

  test.each([
    [
      '2026-01-31T16:05:00Z',
      'Asia/Shanghai',
      { hour: 0, minute: 5, weekday: 0, month: 2, day: 1 },
    ],
    [
      '2026-03-08T06:59:00Z',
      'America/New_York',
      { hour: 1, minute: 59, weekday: 0, month: 3, day: 8 },
    ],
    [
      '2026-03-08T07:00:00Z',
      'America/New_York',
      { hour: 3, minute: 0, weekday: 0, month: 3, day: 8 },
    ],
    [
      '2026-09-07T01:02:00Z',
      '',
      { hour: 1, minute: 2, weekday: 1, month: 9, day: 7 },
    ],
    [
      '2026-09-07T01:02:00Z',
      'Invalid/Zone',
      { hour: 1, minute: 2, weekday: 1, month: 9, day: 7 },
    ],
  ])('resolves the full local date at %s in %s', (now, zone, expected) => {
    expect(timeInBillingZone(new Date(now), zone)).toEqual(expected)
  })
  test.each([
    ['2026-09-07T08:59:59+08:00', 'off_peak', 1955],
    ['2026-09-07T09:00:00+08:00', 'peak', 3910],
    ['2026-09-07T11:59:59+08:00', 'peak', 3910],
    ['2026-09-07T12:00:00+08:00', 'off_peak', 1955],
    ['2026-09-07T14:00:00+08:00', 'peak', 3910],
    ['2026-09-07T17:59:59+08:00', 'peak', 3910],
    ['2026-09-07T18:00:00+08:00', 'off_peak', 1955],
    ['2026-09-12T10:00:00+08:00', 'off_peak', 1955],
    ['2026-09-13T10:00:00+08:00', 'off_peak', 1955],
  ])(
    'selects %s as %s without a missing time function',
    (now, matchedTier, cost) => {
      vi.useFakeTimers()
      vi.setSystemTime(new Date(now))
      expect(evalExprLocally(deepSeekExpression, 1000, 100, extras)).toEqual({
        cost,
        matchedTier,
        error: null,
      })
    }
  )
})

describe('visual billing document', () => {
  test.each([
    deepSeekExpression,
    `v1:  (hour('Asia/Shanghai') >= 9 ? tier('peak', cr * 0 + p * 3e0 + c * 9) : tier('off', c * 4.5 + p * 1.5))  `,
    '!(weekday("UTC") == 0 || month("UTC") != 9) ? (len < 100 ? tier("short", p * 2 + c * 4) : tier("long", p * 3 + c * 6)) : tier("off", p * 1)',
    'day("UTC") >= 1 && minute("UTC") < 60 ? tier("on", p * 2) : tier("off", p * 0)',
  ])('preserves the entire source without edits: %s', (source) => {
    const document = parseVisualBillingDocument(source)
    assert(document)
    expect(serializeVisualBillingDocument(document)).toEqual({
      ok: true,
      source,
    })
  })

  test('changes only the edited price span, keeping zero cache price, order and escaped labels', () => {
    const source =
      'v1: hour("Asia/Shanghai") >= 9 ? tier("peak", p * 3 + cr * 0.00 + c * 9e0) : tier("off", c * 4.5 + p * 1.5)'
    const document = parseVisualBillingDocument(source)
    assert(document)
    if (document.root.kind !== 'branch' || document.root.yes.kind !== 'tier') {
      throw new Error('Expected peak tier')
    }
    const tier = document.root.yes
    tier.prices[2] = { ...tier.prices[2], value: '10' }
    expect(serializeVisualBillingDocument(document)).toEqual({
      ok: true,
      source: source.replace('c * 9e0', 'c * 10'),
    })
    tier.label = 'peak "quoted" \n'
    const result = serializeVisualBillingDocument(document)
    expect(result.ok).toBe(true)
    if (!result.ok) return
    expect(
      evaluateBillingExpression(result.source, {
        now: new Date('2026-09-07T10:00:00+08:00'),
        tokens: { p: 100, cr: 50, c: 10 },
      })
    ).toMatchObject({ status: 'success', cost: 400, matchedTier: tier.label })
    expect(result.source).toContain('cr * 0.00')
    expect(parseVisualBillingDocument(result.source)).not.toBeNull()
  })

  test.each([
    ['2026-09-07T08:59:00+08:00', 'off_peak', 150],
    ['2026-09-07T09:00:00+08:00', 'peak', 300],
    ['2026-09-07T12:00:00+08:00', 'off_peak', 150],
    ['2026-09-07T14:00:00+08:00', 'peak', 300],
    ['2026-09-07T18:00:00+08:00', 'off_peak', 150],
    ['2026-09-12T10:00:00+08:00', 'off_peak', 150],
  ])('keeps DeepSeek boundaries at %s', (now, matchedTier, cost) => {
    const document = parseVisualBillingDocument(deepSeekExpression)
    assert(document)
    const result = serializeVisualBillingDocument(document)
    if (!result.ok) throw new Error('Expected valid document')
    expect(
      evaluateBillingExpression(result.source, {
        now: new Date(now),
        tokens: { p: 100, c: 0, cr: 0 },
      })
    ).toMatchObject({ status: 'success', matchedTier, cost })
  })

  test('preserves crossing-midnight OR and 24:00 exclusive versus inclusive boundaries after edits', () => {
    const source =
      '(hour("UTC") >= 21 || hour("UTC") < 6) ? tier("night", p * 1) : tier("day", p * 2)'
    const document = parseVisualBillingDocument(source)
    assert(document)
    if (
      document.root.kind !== 'branch' ||
      document.root.condition.kind !== 'any'
    ) {
      throw new Error('Expected range')
    }
    const range = document.root.condition
    const end = range.children[1]
    if (end.kind !== 'comparison') throw new Error('Expected end')
    range.children[1] = { ...end, operator: '<=', value: '6' }
    const inclusive = serializeVisualBillingDocument(document)
    if (!inclusive.ok) throw new Error('Expected valid inclusive end')
    for (const hour of ['00', '06', '21', '23']) {
      expect(
        evaluateBillingExpression(inclusive.source, {
          now: new Date(`2026-09-07T${hour}:00:00Z`),
          tokens: { p: 100 },
        })
      ).toMatchObject({ status: 'success', matchedTier: 'night', cost: 100 })
    }
    document.root.condition = {
      ...range,
      kind: 'all',
      children: [range.children[0], { ...end, value: '24' }],
    }
    const untilMidnight = serializeVisualBillingDocument(document)
    if (!untilMidnight.ok) throw new Error('Expected valid midnight end')
    expect(
      evaluateBillingExpression(untilMidnight.source, {
        now: new Date('2026-09-07T23:59:00Z'),
        tokens: { p: 100 },
      })
    ).toMatchObject({ status: 'success', matchedTier: 'night' })
    expect(
      evaluateBillingExpression(untilMidnight.source, {
        now: new Date('2026-09-08T00:00:00Z'),
        tokens: { p: 100 },
      })
    ).toMatchObject({ status: 'success', matchedTier: 'day' })
  })

  test('retains nested token tiers when replacing a condition with a negated group', () => {
    const document = parseVisualBillingDocument(
      'hour("UTC") >= 9 ? (len < 100 ? tier("short", p * 1) : tier("long", p * 2)) : tier("off", p * 3)'
    )
    assert(document)
    if (document.root.kind !== 'branch') throw new Error('Expected branch')
    document.root.condition = {
      id: visualNodeId(),
      kind: 'not',
      child: {
        id: visualNodeId(),
        kind: 'any',
        children: [
          document.root.condition,
          {
            id: visualNodeId(),
            kind: 'comparison',
            probe: 'weekday',
            timezone: 'UTC',
            operator: '==',
            value: '0',
          },
        ],
      },
    }
    const result = serializeVisualBillingDocument(document)
    if (!result.ok) throw new Error('Expected negated branch')
    expect(
      evaluateBillingExpression(result.source, {
        now: new Date('2026-09-07T08:00:00Z'),
        tokens: { p: 100, len: 200 },
      })
    ).toMatchObject({ status: 'success', matchedTier: 'long', cost: 200 })
    expect(
      evaluateBillingExpression(result.source, {
        now: new Date('2026-09-06T08:00:00Z'),
        tokens: { p: 100, len: 20 },
      })
    ).toMatchObject({ status: 'success', matchedTier: 'off', cost: 300 })
  })

  test('rejects incomplete groups, invalid prices and invalid time bounds without returning partial source', () => {
    const document = parseVisualBillingDocument(deepSeekExpression)
    assert(document)
    if (document.root.kind !== 'branch') throw new Error('Expected branch')
    const condition = document.root.condition
    for (const value of ['', '-1', 'NaN', 'Infinity', '1.5', '25']) {
      document.root.condition = {
        id: visualNodeId(),
        kind: 'comparison',
        probe: 'hour',
        timezone: 'UTC',
        operator: '<',
        value,
      }
      expect(serializeVisualBillingDocument(document)).toMatchObject({
        ok: false,
      })
    }
    document.root.condition = {
      id: visualNodeId(),
      kind: 'all',
      children: [],
    } satisfies VisualCondition
    expect(serializeVisualBillingDocument(document)).toMatchObject({
      ok: false,
    })
    document.root.condition = condition
    if (document.root.yes.kind !== 'tier') throw new Error('Expected peak tier')
    document.root.yes.prices[0].value = ''
    expect(serializeVisualBillingDocument(document)).toMatchObject({
      ok: false,
    })
    expect(document.source).toBe(deepSeekExpression)
    expect(
      parseVisualBillingDocument(
        'hour("UTC") > 9 ? max(p * 2, 1) : tier("off", p * 1)'
      )
    ).toBeNull()
  })
})
