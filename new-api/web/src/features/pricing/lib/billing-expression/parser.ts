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
import {
  BILLING_FUNCTIONS,
  BILLING_VARIABLES,
  TIME_FUNCTIONS,
  BillingExpressionError,
  expressionDependencies,
  expressionFailure,
  visitExpression,
  type CompilationResult,
  type CompiledBillingExpression,
  type ExpressionNode,
  type BillingVariable,
} from './types'

type Lexeme = {
  text: string
  start: number
  end: number
  value?: string | number
  integer?: boolean
  kind: 'number' | 'string' | 'symbol' | 'name' | 'end'
}
const PRECEDENCE: Readonly<Record<string, number>> = {
  '||': 1,
  '&&': 2,
  '==': 3,
  '!=': 3,
  '<': 4,
  '<=': 4,
  '>': 4,
  '>=': 4,
  '+': 5,
  '-': 5,
  '*': 6,
  '/': 6,
  '%': 6,
}

class BillingParser {
  private cursor = 0
  private count = 0
  private depth = 0
  private current: Lexeme

  constructor(private readonly source: string) {
    if (source.length > 65536) {
      throw new BillingExpressionError({
        code: 'limit',
        detail: 'expression length',
      })
    }
    const version = source.match(/^\s*v(\d+):/)
    if (version && version[1] !== '1') {
      throw new BillingExpressionError({
        code: 'unsupported',
        detail: version[0],
      })
    }
    if (version) this.cursor = version[0].length
    this.current = this.next()
  }

  private next(): Lexeme {
    if (++this.count > 10000) {
      throw new BillingExpressionError({
        code: 'limit',
        detail: 'expression tokens',
      })
    }
    while (
      /\s/.test(this.source[this.cursor] ?? '') &&
      this.cursor < this.source.length
    ) {
      this.cursor++
    }
    const start = this.cursor
    const rest = this.source.slice(start)
    if (!rest) return { kind: 'end', text: '', start, end: start }
    const number = rest.match(/^(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?/)
    if (number) {
      const value = Number(number[0])
      const integer = !/[.eE]/.test(number[0])
      if (!Number.isFinite(value)) {
        throw new BillingExpressionError({
          code: 'number',
          detail: number[0],
          position: start,
        })
      }
      if (integer && !Number.isSafeInteger(value)) {
        throw new BillingExpressionError({
          code: 'unsupported',
          detail: 'integer precision',
          position: start,
        })
      }
      this.cursor += number[0].length
      return {
        kind: 'number',
        text: number[0],
        value,
        integer,
        start,
        end: this.cursor,
      }
    }
    if (rest[0] === '"' || rest[0] === "'") {
      const quote = rest[0]
      this.cursor++
      let value = ''
      while (this.cursor < this.source.length) {
        const char = this.source[this.cursor++]
        if (char === quote) {
          return {
            kind: 'string',
            text: this.source.slice(start, this.cursor),
            value,
            start,
            end: this.cursor,
          }
        }
        if (char === '\n' || char === '\r') break
        if (char !== '\\') {
          value += char
          continue
        }
        const escaped = this.source[this.cursor++]
        const escapes: Record<string, string> = {
          n: '\n',
          r: '\r',
          t: '\t',
          b: '\b',
          f: '\f',
          v: '\v',
          '\\': '\\',
          '"': '"',
          "'": "'",
          '/': '/',
        }
        if (Object.hasOwn(escapes, escaped)) {
          value += escapes[escaped]
          continue
        }
        if (escaped === 'u' || escaped === 'x') {
          const length = escaped === 'u' ? 4 : 2
          const hex = this.source.slice(this.cursor, this.cursor + length)
          if (hex.length !== length || !/^[\da-f]+$/i.test(hex)) break
          value += String.fromCharCode(Number.parseInt(hex, 16))
          this.cursor += length
          continue
        }
        throw new BillingExpressionError({
          code: 'unsupported',
          detail: 'string escape',
          position: this.cursor - 1,
        })
      }
      throw new BillingExpressionError({
        code: 'syntax',
        detail: 'unterminated string',
        position: start,
      })
    }
    const name = rest.match(/^[A-Za-z_][\w]*/)
    if (name) {
      this.cursor += name[0].length
      return { kind: 'name', text: name[0], start, end: this.cursor }
    }
    const operator = rest.match(/^(?:&&|\|\||==|!=|<=|>=|[+\-*/%<>()!?:,])/)
    if (!operator) {
      throw new BillingExpressionError({
        code: 'unsupported',
        detail: rest[0],
        position: start,
      })
    }
    this.cursor += operator[0].length
    return { kind: 'symbol', text: operator[0], start, end: this.cursor }
  }

  private take(): Lexeme {
    const current = this.current
    this.current = this.next()
    return current
  }

  private is(text: string): boolean {
    return this.current.text === text
  }

  private expect(text: string): Lexeme {
    if (this.current.text !== text) {
      throw new BillingExpressionError({
        code: 'syntax',
        detail: `expected ${text}`,
        position: this.current.start,
      })
    }
    return this.take()
  }

  parse(): ExpressionNode {
    const node = this.expression()
    if (this.current.kind !== 'end') {
      throw new BillingExpressionError({
        code: 'unsupported',
        detail: this.current.text,
        position: this.current.start,
      })
    }
    return node
  }

  private expression(minimum = 0): ExpressionNode {
    if (++this.depth > 128) {
      throw new BillingExpressionError({
        code: 'limit',
        detail: 'expression depth',
      })
    }
    let left = this.primary()
    while (
      Object.hasOwn(PRECEDENCE, this.current.text) &&
      PRECEDENCE[this.current.text] >= minimum
    ) {
      const operator = this.take().text
      const right = this.expression(PRECEDENCE[operator] + 1)
      left = {
        kind: 'binary',
        operator,
        left,
        right,
        start: left.start,
        end: right.end,
      }
    }
    if (minimum === 0 && this.current.text === '?') {
      this.take()
      const yes = this.expression()
      this.expect(':')
      const no = this.expression()
      left = {
        kind: 'conditional',
        condition: left,
        yes,
        no,
        start: left.start,
        end: no.end,
      }
    }
    this.depth--
    return left
  }

  private primary(): ExpressionNode {
    const token = this.take()
    const span = { start: token.start, end: token.end }
    if (token.kind === 'number') {
      return {
        kind: 'literal',
        value: token.value as number,
        integer: token.integer,
        ...span,
      }
    }
    if (token.kind === 'string') {
      return { kind: 'literal', value: token.value as string, ...span }
    }
    if (token.text === '(') {
      const expression = this.expression()
      const end = this.expect(')').end
      return { ...expression, start: token.start, end }
    }
    if (['!', '+', '-'].includes(token.text)) {
      const operand = this.expression(7)
      return {
        kind: 'unary',
        operator: token.text,
        operand,
        start: token.start,
        end: operand.end,
      }
    }
    if (token.kind !== 'name') {
      throw new BillingExpressionError({
        code: 'syntax',
        detail: token.text,
        position: token.start,
      })
    }
    if (token.text === 'nil') return { kind: 'literal', value: null, ...span }
    if (token.text === 'true' || token.text === 'false') {
      return { kind: 'literal', value: token.text === 'true', ...span }
    }
    if (this.current.text !== '(') {
      if (!(BILLING_VARIABLES as readonly string[]).includes(token.text)) {
        throw new BillingExpressionError({
          code: 'unsupported',
          detail: token.text,
          position: token.start,
        })
      }
      return { kind: 'variable', name: token.text as BillingVariable, ...span }
    }
    if (!Object.hasOwn(BILLING_FUNCTIONS, token.text)) {
      throw new BillingExpressionError({
        code: 'unsupported',
        detail: token.text,
        position: token.start,
      })
    }
    this.take()
    const args: ExpressionNode[] = []
    while (!this.is(')')) {
      args.push(this.expression())
      if (!this.is(',')) break
      this.take()
    }
    const end = this.expect(')').end
    if (args.length !== BILLING_FUNCTIONS[token.text]) {
      throw new BillingExpressionError({
        code: 'type',
        detail: token.text,
        position: token.start,
      })
    }
    return { kind: 'call', name: token.text, args, start: token.start, end }
  }
}

type ExpressionType = 'number' | 'string' | 'boolean' | 'nil' | 'dynamic'

function checkExpressionTypes(ast: ExpressionNode): void {
  const types = new Map<ExpressionNode, ExpressionType>()
  visitExpression(ast, (node) => {
    const requireType = (
      child: ExpressionNode,
      expected: ExpressionType
    ): void => {
      const actual = types.get(child)
      if (actual !== expected && actual !== 'dynamic') {
        throw new BillingExpressionError({
          code: 'type',
          detail: `${expected} operand`,
          position: child.start,
        })
      }
    }
    let result: ExpressionType = 'dynamic'
    if (node.kind === 'literal') {
      if (node.value === null) result = 'nil'
      else if (typeof node.value === 'number') result = 'number'
      else if (typeof node.value === 'string') result = 'string'
      else result = 'boolean'
    } else if (node.kind === 'variable') result = 'number'
    else if (node.kind === 'unary') {
      result = node.operator === '!' ? 'boolean' : 'number'
      requireType(node.operand, result)
    } else if (node.kind === 'conditional') {
      requireType(node.condition, 'boolean')
      if (types.get(node.yes) === types.get(node.no)) {
        result = types.get(node.yes) ?? 'dynamic'
      }
    } else if (node.kind === 'binary') {
      if (['&&', '||'].includes(node.operator)) {
        requireType(node.left, 'boolean')
        requireType(node.right, 'boolean')
        result = 'boolean'
      } else if (['==', '!=', '<', '<=', '>', '>='].includes(node.operator)) {
        result = 'boolean'
      } else if (
        node.operator === '+' &&
        types.get(node.left) === 'string' &&
        types.get(node.right) === 'string'
      ) {
        result = 'string'
      } else {
        requireType(node.left, 'number')
        requireType(node.right, 'number')
        result = 'number'
      }
    } else if (node.kind === 'call') {
      if (['param', 'header', 'u', ...TIME_FUNCTIONS].includes(node.name)) {
        requireType(node.args[0], 'string')
      }
      if (node.name === 'header') result = 'string'
      else if (node.name === 'has') {
        requireType(node.args[1], 'string')
        result = 'boolean'
      } else if (node.name === 'tier') {
        requireType(node.args[0], 'string')
        requireType(node.args[1], 'number')
        result = 'number'
      } else if ((TIME_FUNCTIONS as readonly string[]).includes(node.name)) {
        result = 'number'
      } else if (
        ['fixed', 'min', 'max', 'abs', 'ceil', 'floor'].includes(node.name)
      ) {
        node.args.forEach((arg) => requireType(arg, 'number'))
        result = 'number'
      }
    }
    types.set(node, result)
  })
}

const compiledCache = new Map<string, CompilationResult>()

function containsPricingMarker(node: ExpressionNode): boolean {
  const calls = expressionDependencies(node).functions
  return calls.has('tier') || calls.has('fixed')
}

function validateFixedPricingTree(
  node: ExpressionNode,
  rules: CompiledBillingExpression['requestRules']
): void {
  if (node.kind === 'conditional' && !containsPricingMarker(node.condition)) {
    validateFixedPricingTree(node.yes, rules)
    validateFixedPricingTree(node.no, rules)
    return
  }
  if (node.kind === 'binary' && node.operator === '*') {
    for (const [factor, pricing] of [
      [node.right, node.left],
      [node.left, node.right],
    ]) {
      if (factor.kind === 'variable' && factor.name === 'image_count') {
        validateFixedPricingTree(pricing, rules)
        return
      }
      const rule = rules.find((candidate) => candidate.node === factor)
      if (
        rule &&
        rule.multiplier >= 0 &&
        !containsPricingMarker(rule.condition)
      ) {
        validateFixedPricingTree(pricing, rules)
        return
      }
    }
  }
  if (
    node.kind === 'call' &&
    node.name === 'tier' &&
    !containsPricingMarker(node.args[0])
  ) {
    const price = node.args[1]
    if (price.kind === 'call' && price.name === 'fixed') {
      const amount = price.args[0]
      if (
        amount.kind === 'literal' &&
        typeof amount.value === 'number' &&
        amount.value >= 0 &&
        Number.isFinite(amount.value * 1_000_000)
      ) {
        return
      }
    } else if (!containsPricingMarker(price)) return
  }
  throw new BillingExpressionError({
    code: 'type',
    detail:
      'Use tier(name, fixed(amount)) with a finite, non-negative numeric literal; combine pricing branches only with conditions and request multipliers.',
    position: node.start,
  })
}

export function compileBillingExpression(source: string): CompilationResult {
  const cached = compiledCache.get(source)
  if (cached) return cached
  let result: CompilationResult
  try {
    const ast = new BillingParser(source).parse()
    checkExpressionTypes(ast)
    const dependencies = expressionDependencies(ast)
    const requestRules: CompiledBillingExpression['requestRules'] = []
    visitExpression(ast, (node) => {
      if (
        node.kind !== 'conditional' ||
        node.yes.kind !== 'literal' ||
        typeof node.yes.value !== 'number' ||
        node.no.kind !== 'literal' ||
        node.no.value !== 1
      ) {
        return
      }
      const calls = expressionDependencies(node.condition).functions
      if (
        ![...calls].some((name) =>
          ['param', 'header', ...TIME_FUNCTIONS].includes(name)
        )
      ) {
        return
      }
      requestRules.push({
        node,
        condition: node.condition,
        multiplier: node.yes.value,
        cond: source.slice(node.condition.start, node.condition.end),
      })
    })
    if (dependencies.functions.has('fixed')) {
      validateFixedPricingTree(ast, requestRules)
    }
    result = {
      status: 'ready',
      version: 1,
      source,
      ast,
      ...dependencies,
      requestRules,
    }
  } catch (error) {
    result = expressionFailure(error)
  }
  const oldest = compiledCache.keys().next().value
  if (compiledCache.size >= 128 && oldest !== undefined) {
    compiledCache.delete(oldest)
  }
  compiledCache.set(source, result)
  return result
}
