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
export const TOKEN_VARIABLES = [
  'p',
  'c',
  'len',
  'cr',
  'cc',
  'cc1h',
  'img',
  'img_cr',
  'img_o',
  'ai',
  'ao',
] as const
export type TokenVariable = (typeof TOKEN_VARIABLES)[number]
export const BILLING_VARIABLES = [...TOKEN_VARIABLES, 'image_count'] as const
export type BillingVariable = (typeof BILLING_VARIABLES)[number]
export const TIME_FUNCTIONS = [
  'hour',
  'minute',
  'weekday',
  'month',
  'day',
] as const
export type TimeFunction = (typeof TIME_FUNCTIONS)[number]
export const BILLING_FUNCTIONS: Readonly<Record<string, number>> = {
  tier: 2,
  fixed: 1,
  param: 1,
  header: 1,
  has: 2,
  u: 1,
  hour: 1,
  minute: 1,
  weekday: 1,
  month: 1,
  day: 1,
  min: 2,
  max: 2,
  abs: 1,
  ceil: 1,
  floor: 1,
}

export type ExpressionNode = (
  | {
      kind: 'literal'
      value: string | number | boolean | null
      integer?: boolean
    }
  | { kind: 'variable'; name: BillingVariable }
  | { kind: 'call'; name: string; args: ExpressionNode[] }
  | { kind: 'unary'; operator: string; operand: ExpressionNode }
  | {
      kind: 'binary'
      operator: string
      left: ExpressionNode
      right: ExpressionNode
    }
  | {
      kind: 'conditional'
      condition: ExpressionNode
      yes: ExpressionNode
      no: ExpressionNode
    }
) & { start: number; end: number }

export type DiagnosticCode =
  | 'syntax'
  | 'unsupported'
  | 'missing_context'
  | 'type'
  | 'number'
  | 'limit'
export type ExpressionDiagnostic = {
  code: DiagnosticCode
  detail: string
  position?: number
}
export class BillingExpressionError extends Error {
  constructor(readonly diagnostic: ExpressionDiagnostic) {
    super(diagnostic.detail)
  }
}

export type BillingRequestRule = {
  cond: string
  multiplier: number
  matched: boolean
}
export type CompiledBillingExpression = {
  status: 'ready'
  source: string
  version: 1
  ast: ExpressionNode
  variables: ReadonlySet<BillingVariable>
  functions: ReadonlySet<string>
  requestRules: {
    node: ExpressionNode
    condition: ExpressionNode
    multiplier: number
    cond: string
  }[]
}
export type ExpressionFailure = {
  status: 'unsupported' | 'invalid' | 'missing_context'
  diagnostic: ExpressionDiagnostic
}
export type CompilationResult = CompiledBillingExpression | ExpressionFailure
export type BillingSimulationContext = {
  imageCount?: number
  /** Already normalized billable counts; no implicit cache subtraction. */
  tokens?: Partial<Record<TokenVariable, number>>
  /** Absent means unknown. An explicitly provided empty request means empty. */
  request?: { body?: unknown; headers?: Record<string, string> }
  usage?: Record<string, unknown>
  now?: Date
}
export type BillingEvaluationResult =
  | ExpressionFailure
  | {
      status: 'success'
      /** Token expressions are unscaled; task expressions already return cost. */
      cost: number
      billingUnit: 'token' | 'request'
      fixedPrice?: number
      imageCount?: number
      matchedTier: string
      requestRules: BillingRequestRule[]
    }

export function expressionFailure(error: unknown): ExpressionFailure {
  const diagnostic =
    error instanceof BillingExpressionError
      ? error.diagnostic
      : {
          code: 'syntax' as const,
          detail: error instanceof Error ? error.message : String(error),
        }
  let status: ExpressionFailure['status'] = 'invalid'
  if (diagnostic.code === 'missing_context') status = 'missing_context'
  if (diagnostic.code === 'unsupported' || diagnostic.code === 'limit') {
    status = 'unsupported'
  }
  return { status, diagnostic }
}

export function visitExpression(
  node: ExpressionNode,
  visitor: (node: ExpressionNode) => void
): void {
  // Postorder matches the backend's instrumentation; bound traversal as well
  // as parsing so long left-associative expressions cannot exhaust the stack.
  const stack = [{ node, visited: false, depth: 0 }]
  while (stack.length > 0) {
    const entry = stack.pop()
    if (!entry) break
    if (entry.depth > 256) {
      throw new BillingExpressionError({
        code: 'limit',
        detail: 'expression depth',
      })
    }
    if (entry.visited) {
      visitor(entry.node)
      continue
    }
    stack.push({ ...entry, visited: true })
    const children: ExpressionNode[] = []
    if (entry.node.kind === 'call') children.push(...entry.node.args)
    if (entry.node.kind === 'unary') children.push(entry.node.operand)
    if (entry.node.kind === 'binary') {
      children.push(entry.node.left, entry.node.right)
    }
    if (entry.node.kind === 'conditional') {
      children.push(entry.node.condition, entry.node.yes, entry.node.no)
    }
    for (const child of children.reverse()) {
      stack.push({ node: child, visited: false, depth: entry.depth + 1 })
    }
  }
}

export function expressionDependencies(node: ExpressionNode): {
  variables: Set<BillingVariable>
  functions: Set<string>
} {
  const variables = new Set<BillingVariable>()
  const functions = new Set<string>()
  visitExpression(node, (part) => {
    if (part.kind === 'variable') variables.add(part.name)
    if (part.kind === 'call') functions.add(part.name)
  })
  return { variables, functions }
}
