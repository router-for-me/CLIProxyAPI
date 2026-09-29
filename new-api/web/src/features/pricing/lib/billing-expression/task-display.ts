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
import type { BillingUsageSchema } from '../../types'
import type { TaskTier } from './display'
import { compileBillingExpression } from './parser'
import { TIME_FUNCTIONS, visitExpression, type ExpressionNode } from './types'

type PriceBranch = {
  label?: string
  constant: number
  // Actual USD per unit; token display scaling happens only at the boundary.
  prices: Record<string, number>
  times: Map<string, boolean>
}
type TaskDisplayContext = {
  source: string
  schema: BillingUsageSchema
  facts: Record<string, string | boolean>
  remaining: number
}
const MAX_DISPLAY_ROWS = 128

/** Resolve only declared categorical facts. Time predicates remain explicit. */
function taskDisplayCondition(
  node: ExpressionNode,
  context: TaskDisplayContext
): boolean | string | null {
  if (--context.remaining < 0) return null
  if (node.kind === 'literal' && typeof node.value === 'boolean') {
    return node.value
  }
  if (node.kind === 'unary' && node.operator === '!') {
    const value = taskDisplayCondition(node.operand, context)
    if (value === null) return null
    return typeof value === 'boolean' ? !value : `!(${value})`
  }
  if (node.kind !== 'binary') return null
  if (node.operator === '&&' || node.operator === '||') {
    const left = taskDisplayCondition(node.left, context)
    const right = taskDisplayCondition(node.right, context)
    if (left === null || right === null) return null
    const shortCircuit = node.operator === '||'
    if (left === shortCircuit || right === shortCircuit) return shortCircuit
    if (typeof left === 'boolean') return right
    if (typeof right === 'boolean') return left
    return `(${left}) ${node.operator} (${right})`
  }
  if (['==', '!='].includes(node.operator)) {
    for (const [probe, literal] of [
      [node.left, node.right],
      [node.right, node.left],
    ]) {
      if (
        probe.kind !== 'call' ||
        probe.name !== 'u' ||
        probe.args[0].kind !== 'literal' ||
        typeof probe.args[0].value !== 'string' ||
        literal.kind !== 'literal'
      ) {
        continue
      }
      const key = probe.args[0].value
      if (!Object.hasOwn(context.facts, key)) return null
      const definition = context.schema[key]
      if (definition.type === 'boolean') {
        if (typeof literal.value !== 'boolean') return null
      } else if (typeof literal.value !== 'string') {
        return null
      } else if (!definition.enum?.includes(literal.value)) {
        // An enum value the schema no longer declares never matches a request.
        return node.operator === '!='
      }
      const equal = context.facts[key] === literal.value
      return node.operator === '==' ? equal : !equal
    }
  }
  if (
    ['==', '!=', '<', '<=', '>', '>='].includes(node.operator) &&
    [node.left, node.right].some((part) => part.kind === 'call') &&
    [node.left, node.right].every(
      (part) =>
        (part.kind === 'literal' && typeof part.value === 'number') ||
        (part.kind === 'call' &&
          (TIME_FUNCTIONS as readonly string[]).includes(part.name) &&
          part.args[0].kind === 'literal' &&
          typeof part.args[0].value === 'string' &&
          part.args[0].value.trim() !== 'Local')
    )
  ) {
    return context.source.slice(node.start, node.end)
  }
  return null
}

/** Symbolic linear prices, never sample-and-fit or partial coefficient extraction. */
function taskPriceBranches(
  node: ExpressionNode,
  context: TaskDisplayContext
): PriceBranch[] | null {
  if (--context.remaining < 0) return null
  if (
    node.kind === 'literal' &&
    typeof node.value === 'number' &&
    node.value >= 0
  ) {
    return [{ constant: node.value, prices: {}, times: new Map() }]
  }
  if (
    node.kind === 'call' &&
    node.name === 'u' &&
    node.args[0].kind === 'literal' &&
    typeof node.args[0].value === 'string'
  ) {
    const field = node.args[0].value
    if (
      context.schema[field]?.type !== 'number' ||
      !context.schema[field].unit
    ) {
      return null
    }
    return [{ constant: 0, prices: { [field]: 1 }, times: new Map() }]
  }
  if (
    node.kind === 'call' &&
    node.name === 'tier' &&
    node.args[0].kind === 'literal' &&
    typeof node.args[0].value === 'string'
  ) {
    const branches = taskPriceBranches(node.args[1], context)
    if (!branches || branches.some((branch) => branch.label !== undefined)) {
      return null
    }
    const label = node.args[0].value
    return branches.map((branch) => ({ ...branch, label }))
  }
  if (node.kind === 'conditional') {
    const condition = taskDisplayCondition(node.condition, context)
    if (condition === null) return null
    if (typeof condition === 'boolean') {
      return taskPriceBranches(condition ? node.yes : node.no, context)
    }
    const branches: PriceBranch[] = []
    for (const matches of [true, false]) {
      const children = taskPriceBranches(matches ? node.yes : node.no, context)
      if (!children) return null
      for (const child of children) {
        if (
          child.times.has(condition) &&
          child.times.get(condition) !== matches
        ) {
          continue
        }
        branches.push({
          ...child,
          times: new Map([[condition, matches], ...child.times]),
        })
      }
    }
    return branches.length <= MAX_DISPLAY_ROWS ? branches : null
  }
  if (node.kind !== 'binary' || !['+', '*', '/'].includes(node.operator)) {
    return null
  }
  const left = taskPriceBranches(node.left, context)
  const right = taskPriceBranches(node.right, context)
  if (!left || !right || left.length * right.length > MAX_DISPLAY_ROWS) {
    return null
  }
  const branches: PriceBranch[] = []
  for (const a of left) {
    for (const b of right) {
      if (a.label !== undefined && b.label !== undefined) return null
      if (
        [...a.times].some(
          ([key, value]) => b.times.has(key) && b.times.get(key) !== value
        )
      ) {
        continue
      }
      const aQuantity = Object.keys(a.prices).length > 0
      const bQuantity = Object.keys(b.prices).length > 0
      const prices: Record<string, number> = {}
      let constant: number
      if (node.operator === '+') {
        constant = a.constant + b.constant
        for (const field of new Set([
          ...Object.keys(a.prices),
          ...Object.keys(b.prices),
        ])) {
          prices[field] = (a.prices[field] ?? 0) + (b.prices[field] ?? 0)
        }
      } else {
        if (
          (aQuantity && bQuantity) ||
          (node.operator === '/' && (bQuantity || b.constant <= 0))
        ) {
          return null
        }
        const quantity = aQuantity ? a : b
        let factor = aQuantity ? b.constant : a.constant
        if (node.operator === '/') factor = 1 / b.constant
        constant =
          node.operator === '/'
            ? a.constant / b.constant
            : a.constant * b.constant
        for (const [field, price] of Object.entries(quantity.prices)) {
          prices[field] = price * factor
        }
      }
      if (
        ![constant, ...Object.values(prices)].every(
          (value) => Number.isFinite(value) && value >= 0
        )
      ) {
        return null
      }
      branches.push({
        label: a.label ?? b.label,
        constant,
        prices,
        times: new Map([...a.times, ...b.times]),
      })
    }
  }
  return branches
}

/** Display-only adapter. Keep the visual editor's canonical grammar unchanged. */
export function readConditionalTaskPricing(
  source: string,
  schema: BillingUsageSchema
): TaskTier[] | null {
  const compiled = compileBillingExpression(source)
  if (compiled.status !== 'ready') return null
  const fields = new Set<string>()
  let valid = true
  visitExpression(compiled.ast, (node) => {
    if (node.kind !== 'call' || node.name !== 'u') return
    const key = node.args[0]
    if (
      key.kind !== 'literal' ||
      typeof key.value !== 'string' ||
      !Object.hasOwn(schema, key.value)
    ) {
      valid = false
      return
    }
    fields.add(key.value)
  })
  if (!valid) return null
  let combinations: TaskDisplayContext['facts'][] = [{}]
  for (const field of fields) {
    const definition = schema[field]
    if (definition.type === 'number') continue
    const values =
      definition.type === 'boolean' ? [false, true] : definition.enum
    if (
      !values?.length ||
      combinations.length * values.length > MAX_DISPLAY_ROWS
    ) {
      return null
    }
    combinations = combinations.flatMap((facts) =>
      values.map((value) => ({ ...facts, [field]: value }))
    )
  }
  const context: TaskDisplayContext = {
    source,
    schema,
    facts: {},
    remaining: 20000,
  }
  const tiers: TaskTier[] = []
  for (const facts of combinations) {
    context.facts = facts
    const branches = taskPriceBranches(compiled.ast, context)
    if (!branches || tiers.length + branches.length > MAX_DISPLAY_ROWS) {
      return null
    }
    for (const branch of branches) {
      if (branch.label === undefined) return null
      const unitPrices: Record<string, number> = {}
      for (const field of fields) {
        const definition = schema[field]
        if (definition.type !== 'number' || !definition.unit) continue
        unitPrices[field] =
          (branch.prices[field] ?? 0) *
          (definition.unit === 'token' ? 1_000_000 : 1)
      }
      if (
        !Object.keys(unitPrices).length ||
        !Object.values(unitPrices).every(Number.isFinite)
      ) {
        return null
      }
      const conditionText = [...branch.times]
        .map(([condition, matches]) =>
          matches ? `(${condition})` : `!(${condition})`
        )
        .join(' && ')
      tiers.push({
        label: branch.label,
        conditions: Object.entries(facts).map(([field, value]) => ({
          field,
          value: String(value),
        })),
        ...(conditionText ? { conditionText } : {}),
        constant: branch.constant,
        unitPrices,
      })
    }
  }
  return tiers
}
