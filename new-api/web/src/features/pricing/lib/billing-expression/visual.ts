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
import { flattenBinary } from './display'
import { compileBillingExpression } from './parser'
import {
  TIME_FUNCTIONS,
  type ExpressionNode,
  type TimeFunction,
  type TokenVariable,
} from './types'

type Origin = { id: string; origin?: ExpressionNode }
export type VisualComparison = Origin & {
  kind: 'comparison'
  probe: 'p' | 'c' | 'len' | TimeFunction
  timezone: string
  operator: '<' | '<=' | '>' | '>=' | '==' | '!='
  value: string
}
export type VisualCondition =
  | VisualComparison
  | (Origin & { kind: 'all'; children: VisualCondition[] })
  | (Origin & { kind: 'any'; children: VisualCondition[] })
  | (Origin & { kind: 'not'; child: VisualCondition })
export type VisualPrice = {
  variable: Exclude<TokenVariable, 'len'>
  value: string
  origin?: ExpressionNode
}
export type VisualPricingNode =
  | (Origin & {
      kind: 'tier'
      label: string
      prices: VisualPrice[]
      billingUnit: 'token' | 'request'
      fixedPrice: string
    })
  | (Origin & {
      kind: 'branch'
      condition: VisualCondition
      yes: VisualPricingNode
      no: VisualPricingNode
    })
export type VisualBillingDocument = { source: string; root: VisualPricingNode }
export type VisualBillingIssue = { id: string; message: string }
export type VisualBillingSerialization =
  | { ok: true; source: string }
  | { ok: false; issues: VisualBillingIssue[] }

const COMPARISONS = new Set(['<', '<=', '>', '>=', '==', '!='])
const TIME_DOMAINS: Record<TimeFunction, [number, number]> = {
  hour: [0, 23],
  minute: [0, 59],
  weekday: [0, 6],
  month: [1, 12],
  day: [1, 31],
}
let nextId = 0
export function visualNodeId(): string {
  return `draft-${++nextId}`
}

export function createEmptyVisualCondition(): VisualComparison {
  return {
    id: visualNodeId(),
    kind: 'comparison',
    probe: 'hour',
    timezone: 'Asia/Shanghai',
    operator: '>=',
    value: '',
  }
}

function readVisualCondition(node: ExpressionNode): VisualCondition | null {
  const identity = { id: `condition-${node.start}-${node.end}`, origin: node }
  if (node.kind === 'unary' && node.operator === '!') {
    const child = readVisualCondition(node.operand)
    return child ? { ...identity, kind: 'not', child } : null
  }
  if (node.kind === 'binary' && ['&&', '||'].includes(node.operator)) {
    const children = flattenBinary(node, node.operator).map(readVisualCondition)
    if (children.some((child) => child === null)) return null
    return {
      ...identity,
      kind: node.operator === '&&' ? 'all' : 'any',
      children: children as VisualCondition[],
    }
  }
  if (
    node.kind !== 'binary' ||
    !COMPARISONS.has(node.operator) ||
    node.right.kind !== 'literal' ||
    typeof node.right.value !== 'number'
  ) {
    return null
  }
  let probe: VisualComparison['probe']
  let timezone = ''
  if (
    node.left.kind === 'variable' &&
    ['p', 'c', 'len'].includes(node.left.name)
  ) {
    probe = node.left.name as 'p' | 'c' | 'len'
  } else if (
    node.left.kind === 'call' &&
    (TIME_FUNCTIONS as readonly string[]).includes(node.left.name) &&
    node.left.args[0].kind === 'literal' &&
    typeof node.left.args[0].value === 'string'
  ) {
    probe = node.left.name as TimeFunction
    timezone = node.left.args[0].value
  } else return null
  return {
    ...identity,
    kind: 'comparison',
    probe,
    timezone,
    operator: node.operator as VisualComparison['operator'],
    value: String(node.right.value),
  }
}

function readVisualPricing(node: ExpressionNode): VisualPricingNode | null {
  const identity = { id: `pricing-${node.start}-${node.end}`, origin: node }
  if (node.kind === 'conditional') {
    const condition = readVisualCondition(node.condition)
    const yes = readVisualPricing(node.yes)
    const no = readVisualPricing(node.no)
    return condition && yes && no
      ? { ...identity, kind: 'branch', condition, yes, no }
      : null
  }
  if (
    node.kind !== 'call' ||
    node.name !== 'tier' ||
    node.args[0].kind !== 'literal' ||
    typeof node.args[0].value !== 'string'
  ) {
    return null
  }
  const prices: VisualPrice[] = []
  const body = node.args[1]
  if (
    body.kind === 'call' &&
    body.name === 'fixed' &&
    body.args[0].kind === 'literal'
  ) {
    return {
      ...identity,
      kind: 'tier',
      label: node.args[0].value,
      prices,
      billingUnit: 'request',
      fixedPrice: String(body.args[0].value),
    }
  }
  for (const term of flattenBinary(node.args[1], '+')) {
    if (
      term.kind !== 'binary' ||
      term.operator !== '*' ||
      term.left.kind !== 'variable' ||
      term.left.name === 'len' ||
      term.left.name === 'image_count' ||
      term.right.kind !== 'literal' ||
      typeof term.right.value !== 'number' ||
      term.right.value < 0
    ) {
      return null
    }
    const variable = term.left.name
    if (prices.some((price) => price.variable === variable)) return null
    prices.push({
      variable: term.left.name,
      value: String(term.right.value),
      origin: term,
    })
  }
  return {
    ...identity,
    kind: 'tier',
    label: node.args[0].value,
    prices,
    billingUnit: 'token',
    fixedPrice: '',
  }
}

export function parseVisualBillingDocument(
  source: string
): VisualBillingDocument | null {
  const compiled = compileBillingExpression(source)
  if (compiled.status !== 'ready') return null
  const root = readVisualPricing(compiled.ast)
  if (!root) return null
  const document = { source, root }
  return serializeVisualBillingDocument(document).ok ? document : null
}

/** Reuse untouched source spans instead of rewriting whitespace, labels or price order. */
function patchSource(
  source: string,
  origin: ExpressionNode,
  patches: { node: ExpressionNode; text: string }[]
): string {
  let result = source.slice(origin.start, origin.end)
  for (const patch of [...patches].sort(
    (a, b) => b.node.start - a.node.start
  )) {
    result =
      result.slice(0, patch.node.start - origin.start) +
      patch.text +
      result.slice(patch.node.end - origin.start)
  }
  return result
}

function parseNonNegativeNumber(value: string): number | null {
  if (!/^(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$/.test(value.trim())) {
    return null
  }
  const number = Number(value)
  return Number.isFinite(number) && number >= 0 ? number : null
}

function writeVisualCondition(
  node: VisualCondition,
  source: string,
  issues: VisualBillingIssue[]
): string {
  if (node.kind === 'not') {
    const child = writeVisualCondition(node.child, source, issues)
    if (
      node.origin?.kind === 'unary' &&
      node.child.origin === node.origin.operand
    ) {
      return patchSource(source, node.origin, [
        { node: node.origin.operand, text: child },
      ])
    }
    return `!(${child})`
  }
  if (node.kind === 'all' || node.kind === 'any') {
    if (node.children.length === 0) {
      issues.push({
        id: node.id,
        message: 'Add at least one condition to this group.',
      })
    }
    const children = node.children.map((child) =>
      writeVisualCondition(child, source, issues)
    )
    const operator = node.kind === 'all' ? '&&' : '||'
    if (node.origin?.kind === 'binary' && node.origin.operator === operator) {
      const original = flattenBinary(node.origin, operator)
      if (
        original.length === node.children.length &&
        original.every((child, i) => node.children[i].origin === child)
      ) {
        return patchSource(
          source,
          node.origin,
          original.map((child, i) => ({ node: child, text: children[i] }))
        )
      }
    }
    return `(${children.map((child) => `(${child})`).join(` ${operator} `)})`
  }
  const value = parseNonNegativeNumber(node.value)
  let valid = value !== null
  if ((TIME_FUNCTIONS as readonly string[]).includes(node.probe)) {
    const [min, max] = TIME_DOMAINS[node.probe as TimeFunction]
    valid =
      valid &&
      Number.isInteger(value) &&
      Number(value) >= min &&
      Number(value) <= max + (node.operator === '<' ? 1 : 0)
  }
  if (!valid) {
    issues.push({ id: node.id, message: 'Enter a valid condition value.' })
  }
  const isTime = (TIME_FUNCTIONS as readonly string[]).includes(node.probe)
  if (isTime) {
    const originalProbe =
      node.origin?.kind === 'binary' ? node.origin.left : null
    const originalZone =
      originalProbe?.kind === 'call' && originalProbe.args[0].kind === 'literal'
        ? originalProbe.args[0].value
        : null
    if (node.timezone !== originalZone) {
      try {
        if (!node.timezone.trim()) throw new Error('Empty timezone')
        new Intl.DateTimeFormat('en', {
          timeZone: node.timezone.trim(),
        }).format(0)
      } catch {
        issues.push({ id: node.id, message: 'Choose a valid IANA timezone.' })
      }
    }
  }
  const probe = isTime
    ? `${node.probe}(${JSON.stringify(node.timezone)})`
    : node.probe
  const origin = node.origin
  if (origin?.kind === 'binary' && origin.operator === node.operator) {
    const oldProbe = origin.left
    let probeText = probe
    if (oldProbe.kind === 'variable' && oldProbe.name === node.probe) {
      probeText = source.slice(oldProbe.start, oldProbe.end)
    }
    if (
      oldProbe.kind === 'call' &&
      oldProbe.name === node.probe &&
      oldProbe.args[0].kind === 'literal' &&
      oldProbe.args[0].value === node.timezone
    ) {
      probeText = source.slice(oldProbe.start, oldProbe.end)
    }
    const valueText =
      origin.right.kind === 'literal' && value === origin.right.value
        ? source.slice(origin.right.start, origin.right.end)
        : node.value
    return patchSource(source, origin, [
      { node: origin.left, text: probeText },
      { node: origin.right, text: valueText },
    ])
  }
  return `(${probe} ${node.operator} ${node.value})`
}

export function visualConditionExpression(
  node: VisualCondition,
  source: string
): string | null {
  const issues: VisualBillingIssue[] = []
  const expression = writeVisualCondition(node, source, issues)
  return issues.length === 0 ? expression : null
}

function writeVisualPricing(
  node: VisualPricingNode,
  source: string,
  issues: VisualBillingIssue[]
): string {
  if (node.kind === 'branch') {
    const condition = writeVisualCondition(node.condition, source, issues)
    const yes = writeVisualPricing(node.yes, source, issues)
    const no = writeVisualPricing(node.no, source, issues)
    if (node.origin?.kind === 'conditional') {
      return patchSource(source, node.origin, [
        { node: node.origin.condition, text: condition },
        { node: node.origin.yes, text: yes },
        { node: node.origin.no, text: no },
      ])
    }
    return `(${condition}) ? (${yes}) : (${no})`
  }
  if (node.billingUnit === 'request') {
    const value = parseNonNegativeNumber(node.fixedPrice)
    if (value === null || !Number.isFinite(value * 1_000_000)) {
      issues.push({
        id: `${node.id}:fixed`,
        message: 'Enter a finite, non-negative price.',
      })
    }
    const origin = node.origin
    if (origin?.kind === 'call' && origin.name === 'tier') {
      const label = origin.args[0]
      const labelText =
        label.kind === 'literal' && label.value === node.label
          ? source.slice(label.start, label.end)
          : JSON.stringify(node.label)
      const price = origin.args[1]
      let priceText = `fixed(${node.fixedPrice})`
      if (
        price.kind === 'call' &&
        price.name === 'fixed' &&
        price.args[0].kind === 'literal'
      ) {
        const amount = price.args[0]
        const text =
          value === amount.value
            ? source.slice(amount.start, amount.end)
            : node.fixedPrice
        priceText = patchSource(source, price, [{ node: amount, text }])
      }
      return patchSource(source, origin, [
        { node: label, text: labelText },
        { node: price, text: priceText },
      ])
    }
    return `tier(${JSON.stringify(node.label)}, fixed(${node.fixedPrice}))`
  }
  if (node.prices.length === 0) {
    issues.push({
      id: node.id,
      message: 'Include at least one price variable.',
    })
  }
  const terms: string[] = []
  const valuePatches: { node: ExpressionNode; text: string }[] = []
  for (const price of node.prices) {
    const value = parseNonNegativeNumber(price.value)
    if (value === null) {
      issues.push({
        id: `${node.id}:${price.variable}`,
        message: 'Enter a finite, non-negative price.',
      })
    }
    if (
      price.origin?.kind === 'binary' &&
      price.origin.right.kind === 'literal'
    ) {
      const text =
        value === price.origin.right.value
          ? source.slice(price.origin.right.start, price.origin.right.end)
          : price.value
      valuePatches.push({ node: price.origin.right, text })
      terms.push(
        patchSource(source, price.origin, [{ node: price.origin.right, text }])
      )
    } else terms.push(`${price.variable} * ${price.value}`)
  }
  const origin = node.origin
  if (origin?.kind === 'call' && origin.name === 'tier') {
    const label = origin.args[0]
    const labelText =
      label.kind === 'literal' && label.value === node.label
        ? source.slice(label.start, label.end)
        : JSON.stringify(node.label)
    const original = flattenBinary(origin.args[1], '+')
    if (
      original.length === node.prices.length &&
      original.every((term, i) => node.prices[i].origin === term)
    ) {
      return patchSource(source, origin, [
        { node: label, text: labelText },
        ...valuePatches,
      ])
    }
    return patchSource(source, origin, [
      { node: label, text: labelText },
      { node: origin.args[1], text: terms.join(' + ') },
    ])
  }
  return `tier(${JSON.stringify(node.label)}, ${terms.join(' + ')})`
}

export function serializeVisualBillingDocument(
  document: VisualBillingDocument
): VisualBillingSerialization {
  const issues: VisualBillingIssue[] = []
  const body = writeVisualPricing(document.root, document.source, issues)
  if (issues.length > 0) return { ok: false, issues }
  const original = compileBillingExpression(document.source)
  if (original.status !== 'ready') {
    return {
      ok: false,
      issues: [
        {
          id: document.root.id,
          message:
            'This expression cannot be edited visually without losing information.',
        },
      ],
    }
  }
  const source =
    document.source.slice(0, original.ast.start) +
    body +
    document.source.slice(original.ast.end)
  if (compileBillingExpression(source).status !== 'ready') {
    return {
      ok: false,
      issues: [
        { id: document.root.id, message: 'Enter a valid condition value.' },
      ],
    }
  }
  return { ok: true, source }
}
