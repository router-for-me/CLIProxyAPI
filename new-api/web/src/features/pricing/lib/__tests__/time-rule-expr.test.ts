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
import { describe, expect, test } from 'vitest'

import {
  buildRequestRuleExpr,
  MATCH_EQ,
  MATCH_GTE,
  MATCH_RANGE,
  type RequestCondition,
  type RequestRuleGroup,
  type TimeCondition,
  type TimeFunc,
  tryParseRequestRuleExpr,
} from '../billing-expr'

function timeCondition(overrides: Partial<TimeCondition> = {}): TimeCondition {
  return {
    source: 'time',
    timeFunc: 'hour',
    timezone: 'Asia/Shanghai',
    mode: MATCH_RANGE,
    value: '',
    rangeStart: '',
    rangeEnd: '',
    ...overrides,
  }
}

function timeRangeGroup(start: string, end: string): RequestRuleGroup {
  return {
    conditions: [timeCondition({ rangeStart: start, rangeEnd: end })],
    multiplier: '2',
  }
}

function scalarTimeGroup(
  value: string,
  timeFunc: TimeFunc = 'hour'
): RequestRuleGroup {
  return {
    conditions: [timeCondition({ mode: MATCH_GTE, value, timeFunc })],
    multiplier: '2',
  }
}

describe('time range expression generation', () => {
  test('within-day range (start < end) builds an && condition', () => {
    // Regression test for #6923: the || form is a tautology that applies the
    // multiplier 24/7.
    expect(buildRequestRuleExpr([timeRangeGroup('9', '12')])).toBe(
      '(hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12 ? 2 : 1)'
    )
  })

  test('overnight range (start > end) keeps the || condition', () => {
    expect(buildRequestRuleExpr([timeRangeGroup('21', '6')])).toBe(
      '(hour("Asia/Shanghai") >= 21 || hour("Asia/Shanghai") < 6 ? 2 : 1)'
    )
  })

  test('equal bounds build an always-false && range instead of a tautology', () => {
    expect(buildRequestRuleExpr([timeRangeGroup('9', '9')])).toBe(
      '(hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 9 ? 2 : 1)'
    )
  })

  test.each([
    ['out-of-domain negative bounds', '-1', '-5'],
    ['out-of-domain upper bound', '9', '24'],
    ['non-integer bound', '9.5', '12'],
  ])('drops the rule for %s', (_name, start, end) => {
    expect(buildRequestRuleExpr([timeRangeGroup(start, end)])).toBe('')
  })

  test('drops a scalar rule whose value is out of domain', () => {
    expect(buildRequestRuleExpr([scalarTimeGroup('25')])).toBe('')
  })

  test.each([
    ['hour', '0', true],
    ['hour', '23', true],
    ['hour', '24', false],
    ['minute', '59', true],
    ['minute', '60', false],
    ['weekday', '0', true],
    ['weekday', '6', true],
    ['weekday', '7', false],
    ['month', '1', true],
    ['month', '12', true],
    ['month', '0', false],
    ['month', '13', false],
    ['day', '1', true],
    ['day', '31', true],
    ['day', '32', false],
  ])('keeps %s value %s in domain: %s', (timeFunc, value, inDomain) => {
    const expr = buildRequestRuleExpr([
      scalarTimeGroup(value, timeFunc as TimeFunc),
    ])
    expect(expr !== '').toBe(inDomain)
  })
})

describe('time range expression parsing', () => {
  test('parses an && range back into a single MATCH_RANGE condition', () => {
    const groups = tryParseRequestRuleExpr(
      '(hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12 ? 2 : 1)'
    )
    expect(groups).toHaveLength(1)
    expect(groups?.[0].conditions).toHaveLength(1)
    const condition = groups?.[0].conditions[0] as TimeCondition
    expect(condition.mode).toBe(MATCH_RANGE)
    expect(condition.rangeStart).toBe('9')
    expect(condition.rangeEnd).toBe('12')
  })

  test('still parses a legacy || range into a single MATCH_RANGE condition', () => {
    const groups = tryParseRequestRuleExpr(
      '(hour("Asia/Shanghai") >= 21 || hour("Asia/Shanghai") < 6 ? 2 : 1)'
    )
    expect(groups?.[0].conditions).toHaveLength(1)
    const condition = groups?.[0].conditions[0] as TimeCondition
    expect(condition.mode).toBe(MATCH_RANGE)
    expect(condition.rangeStart).toBe('21')
    expect(condition.rangeEnd).toBe('6')
  })

  test('merges adjacent time bounds into MATCH_RANGE when other conditions follow', () => {
    const groups = tryParseRequestRuleExpr(
      '(param("service_tier") == "fast" && hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12 ? 2 : 1)'
    )
    expect(groups?.[0].conditions.map((c) => c.mode)).toEqual([
      MATCH_EQ,
      MATCH_RANGE,
    ])
    const range = groups?.[0].conditions[1] as TimeCondition
    expect(range.rangeStart).toBe('9')
    expect(range.rangeEnd).toBe('12')
  })

  test('keeps a parenthesized overnight range as MATCH_RANGE in a mixed group', () => {
    const groups = tryParseRequestRuleExpr(
      '((hour("Asia/Shanghai") >= 21 || hour("Asia/Shanghai") < 6) && param("service_tier") == "fast" ? 3 : 1)'
    )
    expect(groups?.[0].conditions.map((c) => c.mode)).toEqual([
      MATCH_RANGE,
      MATCH_EQ,
    ])
    expect(groups?.[0].multiplier).toBe('3')
  })

  test('parses the issue #6923 two-scalar workaround groups as single ranges', () => {
    const groups = tryParseRequestRuleExpr(
      '(hour("Asia/Shanghai") >= 9 && hour("Asia/Shanghai") < 12 ? 2 : 1) * (hour("Asia/Shanghai") >= 14 && hour("Asia/Shanghai") < 18 ? 2 : 1)'
    )
    expect(groups).toHaveLength(2)
    for (const group of groups ?? []) {
      expect(group.conditions).toHaveLength(1)
      expect(group.conditions[0].mode).toBe(MATCH_RANGE)
    }
  })

  test.each([
    [
      'out-of-domain range bounds',
      '(hour("Asia/Shanghai") >= 25 && hour("Asia/Shanghai") < 30 ? 2 : 1)',
    ],
    [
      'fractional range bounds',
      '(hour("Asia/Shanghai") >= 1.5 && hour("Asia/Shanghai") < 2.5 ? 2 : 1)',
    ],
    ['out-of-domain scalar value', '(hour("Asia/Shanghai") >= 25 ? 2 : 1)'],
    ['out-of-domain weekday value', '(weekday("UTC") >= 7 ? 2 : 1)'],
  ])('rejects %s instead of parsing them', (_name, expr) => {
    // Rejected rules keep the editor in raw mode; a lenient parse would let
    // the visual editor silently drop the rule on rebuild.
    expect(tryParseRequestRuleExpr(expr)).toBeNull()
  })
})

describe('time range round-trip stability', () => {
  test('build → parse → build yields the identical mixed-group expression', () => {
    const groups: RequestRuleGroup[] = [
      {
        conditions: [
          {
            source: 'param',
            path: 'service_tier',
            mode: MATCH_EQ,
            value: 'fast',
          } satisfies RequestCondition,
          timeCondition({ rangeStart: '9', rangeEnd: '12' }),
        ],
        multiplier: '2',
      },
    ]
    const expr = buildRequestRuleExpr(groups)
    const parsed = tryParseRequestRuleExpr(expr)
    expect(parsed).not.toBeNull()
    expect(buildRequestRuleExpr(parsed ?? [])).toBe(expr)
  })
})
