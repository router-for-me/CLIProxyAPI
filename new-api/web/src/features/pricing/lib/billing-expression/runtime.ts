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
import { compileBillingExpression } from './parser'
import {
  BillingExpressionError,
  TIME_FUNCTIONS,
  expressionFailure,
  type BillingEvaluationResult,
  type BillingRequestRule,
  type BillingSimulationContext,
  type CompiledBillingExpression,
  type ExpressionNode,
  type TimeFunction,
} from './types'

type RuntimeValue = { value: unknown; integer?: boolean }

/** The supported, deterministic subset of GJSON paths (not JavaScript property access). */
export function readRequestPath(body: unknown, rawPath: string): unknown {
  const path = rawPath.trim()
  if (!path) return null
  const parts: string[] = []
  let part = ''
  for (let i = 0; i < path.length; i++) {
    const char = path[i]
    if (char === '\\') {
      if (i + 1 >= path.length) {
        throw new BillingExpressionError({ code: 'unsupported', detail: path })
      }
      part += path[++i]
    } else if (char === '.') {
      parts.push(part)
      part = ''
    } else {
      if ('*?[]()|@!'.includes(char)) {
        throw new BillingExpressionError({ code: 'unsupported', detail: path })
      }
      part += char
    }
  }
  parts.push(part)
  let value = body
  for (let i = 0; i < parts.length; i++) {
    const key = parts[i]
    if (Array.isArray(value)) {
      if (key === '#' && i === parts.length - 1) return value.length
      if (key === '#') {
        throw new BillingExpressionError({ code: 'unsupported', detail: path })
      }
      if (!/^\d+$/.test(key)) return null
      const index = Number(key)
      value = Number.isSafeInteger(index) ? value[index] : undefined
    } else if (
      value != null &&
      typeof value === 'object' &&
      Object.hasOwn(value, key)
    ) {
      value = (value as Record<string, unknown>)[key]
    } else {
      return null
    }
  }
  return value ?? null
}

/** Match fmt.Sprint for JSON values passed to the backend's has() helper. */
function billingString(value: unknown, depth = 0): string {
  if (depth > 128) {
    throw new BillingExpressionError({
      code: 'limit',
      detail: 'request value depth',
    })
  }
  if (value == null) return '<nil>'
  if (Array.isArray(value)) {
    return `[${value.map((item) => billingString(item, depth + 1)).join(' ')}]`
  }
  if (typeof value === 'object') {
    return `map[${Object.entries(value)
      .sort(([a], [b]) => (a < b ? -1 : Number(a > b)))
      .map(([key, item]) => `${key}:${billingString(item, depth + 1)}`)
      .join(' ')}]`
  }
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) {
      throw new BillingExpressionError({
        code: 'number',
        detail: 'request number',
      })
    }
    if (Object.is(value, -0)) return '-0'
    const [mantissa, exponent] = value.toExponential().split('e')
    if (Number(exponent) >= 6 || Number(exponent) < -4) {
      return `${mantissa}e${Number(exponent) < 0 ? '-' : '+'}${Math.abs(Number(exponent)).toString().padStart(2, '0')}`
    }
  }
  return String(value)
}

export function timeInBillingZone(
  now: Date,
  zone: string
): Record<TimeFunction, number> {
  if (!Number.isFinite(now.getTime())) {
    throw new BillingExpressionError({
      code: 'type',
      detail: 'simulation time',
    })
  }
  const timezone = zone.trim() || 'UTC'
  // Go's Local refers to the server's process zone, which the browser cannot know.
  if (timezone === 'Local') {
    throw new BillingExpressionError({
      code: 'unsupported',
      detail: 'server Local timezone',
    })
  }
  let formatter: Intl.DateTimeFormat
  try {
    if (!/^[A-Z][A-Za-z0-9_+-]*(?:\/[A-Z][A-Za-z0-9_+-]*)*$/.test(timezone)) {
      throw new RangeError('timezone')
    }
    formatter = new Intl.DateTimeFormat('en-US', {
      timeZone: timezone,
      calendar: 'gregory',
      numberingSystem: 'latn',
      hourCycle: 'h23',
      hour: 'numeric',
      minute: 'numeric',
      weekday: 'short',
      month: 'numeric',
      day: 'numeric',
    })
    const resolved = formatter.resolvedOptions().timeZone
    if (
      resolved.toLowerCase() === timezone.toLowerCase() &&
      resolved !== timezone
    ) {
      throw new RangeError('timezone')
    }
  } catch {
    return {
      hour: now.getUTCHours(),
      minute: now.getUTCMinutes(),
      weekday: now.getUTCDay(),
      month: now.getUTCMonth() + 1,
      day: now.getUTCDate(),
    }
  }
  const parts = Object.fromEntries(
    formatter.formatToParts(now).map((part) => [part.type, part.value])
  )
  return {
    hour: Number(parts.hour),
    minute: Number(parts.minute),
    weekday: ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'].indexOf(
      parts.weekday
    ),
    month: Number(parts.month),
    day: Number(parts.day),
  }
}

class BillingRuntime {
  readonly requestRules: BillingRequestRule[]
  matchedTier = ''
  billingUnit: 'token' | 'request' = 'token'
  fixedPrice?: number
  private readonly now: Date
  private readonly timeValues = new Map<string, Record<TimeFunction, number>>()
  private readonly headers = new Map<string, string>()
  private depth = 0

  constructor(
    private readonly compiled: CompiledBillingExpression,
    private readonly context: BillingSimulationContext
  ) {
    this.now = context.now ?? new Date()
    this.requestRules = compiled.requestRules.map((rule) => ({
      cond: rule.cond,
      multiplier: rule.multiplier,
      matched: false,
    }))
    for (const [name, rawValue] of Object.entries(
      context.request?.headers ?? {}
    )) {
      if (typeof rawValue !== 'string') {
        throw new BillingExpressionError({
          code: 'type',
          detail: 'request header',
        })
      }
      const key = name.trim().toLowerCase()
      const value = rawValue.trim()
      if (key && value) this.headers.set(key, value)
    }
  }

  evaluate(node: ExpressionNode): RuntimeValue {
    if (++this.depth > 256) {
      throw new BillingExpressionError({
        code: 'limit',
        detail: 'evaluation depth',
      })
    }
    try {
      const value = this.evaluateNode(node)
      if (typeof value.value === 'number') {
        if (!Number.isFinite(value.value)) {
          throw new BillingExpressionError({
            code: 'number',
            detail: 'non-finite result',
            position: node.start,
          })
        }
        if (value.integer && !Number.isSafeInteger(value.value)) {
          throw new BillingExpressionError({
            code: 'unsupported',
            detail: 'integer precision',
            position: node.start,
          })
        }
      }
      return value
    } finally {
      this.depth--
    }
  }

  private evaluateNode(node: ExpressionNode): RuntimeValue {
    if (node.kind === 'literal') {
      return { value: node.value, integer: node.integer }
    }
    if (node.kind === 'variable') {
      if (node.name === 'image_count') {
        const value = this.context.imageCount ?? 1
        if (!Number.isInteger(value) || value < 1 || value > 128) {
          throw new BillingExpressionError({
            code: 'number',
            detail: 'image_count must be between 1 and 128',
            position: node.start,
          })
        }
        return { value }
      }
      const value = this.context.tokens?.[node.name]
      if (value === undefined) {
        throw new BillingExpressionError({
          code: 'missing_context',
          detail: node.name,
          position: node.start,
        })
      }
      if (typeof value !== 'number' || value < 0) {
        throw new BillingExpressionError({
          code: 'number',
          detail: node.name,
          position: node.start,
        })
      }
      return { value }
    }
    if (node.kind === 'conditional') {
      const condition = this.boolean(node.condition)
      const ruleIndex = this.compiled.requestRules.findIndex(
        (rule) => rule.node === node
      )
      if (condition && ruleIndex >= 0) {
        this.requestRules[ruleIndex].matched = true
      }
      return this.evaluate(condition ? node.yes : node.no)
    }
    if (node.kind === 'unary') {
      if (node.operator === '!') return { value: !this.boolean(node.operand) }
      const operand = this.numeric(node.operand)
      return {
        value: node.operator === '-' ? -operand.value : operand.value,
        integer: operand.integer,
      }
    }
    if (node.kind === 'binary') return this.binary(node)
    return this.call(node)
  }

  private boolean(node: ExpressionNode): boolean {
    const result = this.evaluate(node).value
    if (typeof result !== 'boolean') {
      throw new BillingExpressionError({
        code: 'type',
        detail: 'boolean condition',
        position: node.start,
      })
    }
    return result
  }

  private numeric(node: ExpressionNode): { value: number; integer?: boolean } {
    const result = this.evaluate(node)
    if (typeof result.value !== 'number') {
      throw new BillingExpressionError({
        code: 'type',
        detail: 'numeric operand',
        position: node.start,
      })
    }
    return { value: result.value, integer: result.integer }
  }

  private string(node: ExpressionNode): string {
    const result = this.evaluate(node).value
    if (typeof result !== 'string') {
      throw new BillingExpressionError({
        code: 'type',
        detail: 'string argument',
        position: node.start,
      })
    }
    return result
  }

  private binary(
    node: Extract<ExpressionNode, { kind: 'binary' }>
  ): RuntimeValue {
    if (node.operator === '&&') {
      return { value: this.boolean(node.left) && this.boolean(node.right) }
    }
    if (node.operator === '||') {
      return { value: this.boolean(node.left) || this.boolean(node.right) }
    }
    const left = this.evaluate(node.left)
    const right = this.evaluate(node.right)
    const a = left.value
    const b = right.value
    if (node.operator === '==' || node.operator === '!=') {
      if (
        a !== null &&
        b !== null &&
        (typeof a === 'object' || typeof b === 'object')
      ) {
        throw new BillingExpressionError({
          code: 'unsupported',
          detail: 'object comparison',
          position: node.start,
        })
      }
      const equal = a === b
      return { value: node.operator === '==' ? equal : !equal }
    }
    if (
      typeof a === 'string' &&
      typeof b === 'string' &&
      node.operator === '+'
    ) {
      return { value: a + b }
    }
    if (
      (typeof a === 'number' && typeof b === 'number') ||
      (typeof a === 'string' && typeof b === 'string')
    ) {
      switch (node.operator) {
        case '<':
          return { value: a < b }
        case '<=':
          return { value: a <= b }
        case '>':
          return { value: a > b }
        case '>=':
          return { value: a >= b }
      }
    }
    if (typeof a !== 'number' || typeof b !== 'number') {
      throw new BillingExpressionError({
        code: 'type',
        detail: node.operator,
        position: node.start,
      })
    }
    const integer = Boolean(left.integer && right.integer)
    switch (node.operator) {
      case '+':
        return { value: a + b, integer }
      case '-':
        return { value: a - b, integer }
      case '*':
        return { value: a * b, integer }
      case '/':
        return { value: a / b }
      case '%':
        if (!integer) {
          throw new BillingExpressionError({
            code: 'type',
            detail: 'integer modulo',
            position: node.start,
          })
        }
        return { value: a % b, integer: true }
      default:
        throw new BillingExpressionError({
          code: 'unsupported',
          detail: node.operator,
          position: node.start,
        })
    }
  }

  private call(node: Extract<ExpressionNode, { kind: 'call' }>): RuntimeValue {
    const args = node.args
    switch (node.name) {
      case 'fixed': {
        const amount = this.numeric(args[0]).value
        this.billingUnit = 'request'
        this.fixedPrice = amount
        return { value: amount * 1_000_000 }
      }
      case 'tier': {
        const name = this.string(args[0])
        const value = this.numeric(args[1]).value
        this.matchedTier = name
        return { value }
      }
      case 'param': {
        const path = this.string(args[0])
        if (!this.context.request) {
          throw new BillingExpressionError({
            code: 'missing_context',
            detail: 'request body',
            position: node.start,
          })
        }
        return { value: readRequestPath(this.context.request.body, path) }
      }
      case 'header': {
        const name = this.string(args[0]).trim().toLowerCase()
        if (!this.context.request) {
          throw new BillingExpressionError({
            code: 'missing_context',
            detail: 'request headers',
            position: node.start,
          })
        }
        return { value: this.headers.get(name) ?? '' }
      }
      case 'u': {
        const name = this.string(args[0])
        if (!this.context.usage) {
          throw new BillingExpressionError({
            code: 'missing_context',
            detail: 'task usage',
            position: node.start,
          })
        }
        const value = Object.hasOwn(this.context.usage, name)
          ? this.context.usage[name]
          : null
        return { value }
      }
      case 'has': {
        const source = this.evaluate(args[0]).value
        const substring = this.string(args[1])
        return {
          value:
            source != null &&
            substring !== '' &&
            billingString(source).includes(substring),
        }
      }
      case 'min':
        return {
          value: Math.min(
            this.numeric(args[0]).value,
            this.numeric(args[1]).value
          ),
        }
      case 'max':
        return {
          value: Math.max(
            this.numeric(args[0]).value,
            this.numeric(args[1]).value
          ),
        }
      case 'abs':
        return { value: Math.abs(this.numeric(args[0]).value) }
      case 'floor':
        return { value: Math.floor(this.numeric(args[0]).value) }
      case 'ceil':
        return { value: Math.ceil(this.numeric(args[0]).value) }
    }
    if (!(TIME_FUNCTIONS as readonly string[]).includes(node.name)) {
      throw new BillingExpressionError({
        code: 'unsupported',
        detail: node.name,
      })
    }
    const zone = this.string(args[0]).trim()
    let values = this.timeValues.get(zone)
    if (!values) {
      values = timeInBillingZone(this.now, zone)
      this.timeValues.set(zone, values)
    }
    return { value: values[node.name as TimeFunction], integer: true }
  }
}

/** Also used by display adapters to resolve only known, time-dependent conditions. */
export function evaluateBillingCondition(
  compiled: CompiledBillingExpression,
  node: ExpressionNode,
  now: Date
): boolean | null {
  try {
    const result = new BillingRuntime(compiled, { now }).evaluate(node).value
    return typeof result === 'boolean' ? result : null
  } catch {
    return null
  }
}

export function evaluateBillingExpression(
  expression: string | CompiledBillingExpression,
  context: BillingSimulationContext = {}
): BillingEvaluationResult {
  const compiled =
    typeof expression === 'string'
      ? compileBillingExpression(expression)
      : expression
  if (compiled.status !== 'ready') return compiled
  try {
    for (const [name, value] of Object.entries(context.tokens ?? {})) {
      if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) {
        throw new BillingExpressionError({ code: 'number', detail: name })
      }
    }
    const runtime = new BillingRuntime(compiled, context)
    const result = runtime.evaluate(compiled.ast).value
    if (typeof result !== 'number' || !Number.isFinite(result) || result < 0) {
      throw new BillingExpressionError({
        code: 'number',
        detail: 'billing result',
      })
    }
    return {
      status: 'success',
      cost: result,
      billingUnit: runtime.billingUnit,
      ...(compiled.variables.has('image_count')
        ? { imageCount: context.imageCount ?? 1 }
        : {}),
      ...(runtime.fixedPrice !== undefined
        ? { fixedPrice: runtime.fixedPrice }
        : {}),
      matchedTier: runtime.matchedTier,
      requestRules: runtime.requestRules,
    }
  } catch (error) {
    return expressionFailure(error)
  }
}
