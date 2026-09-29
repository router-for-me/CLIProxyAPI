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
const intlConstructors = new Set([
  'NumberFormat',
  'DateTimeFormat',
  'RelativeTimeFormat',
  'Collator',
  'DisplayNames',
  'ListFormat',
  'PluralRules',
  'Segmenter',
  'Locale',
])
const localeMethods = new Set([
  'toLocaleString',
  'toLocaleDateString',
  'toLocaleTimeString',
])
const formatterLocaleArguments = new Map([
  ['formatNumber', 1],
  ['formatCompactNumber', 1],
  ['formatTimestampRelative', 2],
])
const interfaceLanguageProperties = new Set(['language', 'resolvedLanguage'])

function propertyName(node) {
  const property = node.property ?? node.key
  return node.computed ? property?.value : (property?.name ?? property?.value)
}

function findVariable(node, sourceCode) {
  for (let scope = sourceCode.getScope(node); scope; scope = scope.upper) {
    const variable = scope.set.get(node.name)
    if (variable) return variable
  }
}

function callName(node, sourceCode) {
  if (node.type === 'MemberExpression') return propertyName(node)
  if (node.type !== 'Identifier') return undefined
  const binding = findVariable(node, sourceCode)?.defs.find(
    (definition) => definition.type === 'ImportBinding'
  )
  return binding?.node.imported?.name ?? node.name
}

function hasUnsafeLocale(node, sourceCode, visited = new Set()) {
  if (!node || visited.has(node)) return false
  visited.add(node)

  if (
    node.type === 'Literal' ||
    (node.type === 'TemplateLiteral' && node.expressions.length === 0)
  ) {
    const value =
      node.type === 'Literal' ? node.value : node.quasis[0].value.cooked
    if (typeof value !== 'string') return false
    try {
      Intl.getCanonicalLocales(value)
      return false
    } catch {
      return true
    }
  }
  if (node.type === 'MemberExpression') {
    return (
      interfaceLanguageProperties.has(propertyName(node)) ||
      hasUnsafeLocale(node.object, sourceCode, visited)
    )
  }
  if (node.type === 'Identifier') {
    const variable = findVariable(node, sourceCode)
    if (!variable) return false
    for (const definition of variable.defs) {
      const pattern = definition.node.id
      if (pattern?.type !== 'ObjectPattern') continue
      for (const property of pattern.properties) {
        if (property.type !== 'Property') continue
        const binding =
          property.value.type === 'AssignmentPattern'
            ? property.value.left
            : property.value
        if (
          binding.name === node.name &&
          interfaceLanguageProperties.has(propertyName(property))
        ) {
          return true
        }
      }
    }
    // Follow local assignments and aliases without confusing shadowed bindings.
    return variable.references.some((reference) =>
      hasUnsafeLocale(reference.writeExpr, sourceCode, visited)
    )
  }
  if (
    node.type === 'CallExpression' &&
    callName(node.callee, sourceCode) === 'toIntlLocale'
  ) {
    return false
  }
  if (node.type === 'ConditionalExpression') {
    return (
      hasUnsafeLocale(node.consequent, sourceCode, visited) ||
      hasUnsafeLocale(node.alternate, sourceCode, visited)
    )
  }
  if (
    [
      'TSAsExpression',
      'TSTypeAssertion',
      'TSNonNullExpression',
      'TSSatisfiesExpression',
      'ChainExpression',
    ].includes(node.type)
  ) {
    return hasUnsafeLocale(node.expression, sourceCode, visited)
  }
  return (sourceCode.visitorKeys[node.type] ?? []).some((key) => {
    const children = Array.isArray(node[key]) ? node[key] : [node[key]]
    return children.some((child) => hasUnsafeLocale(child, sourceCode, visited))
  })
}

export default {
  rules: {
    'intl-locale': {
      meta: {
        type: 'problem',
        schema: [],
        messages: {
          unsafe:
            'Convert interface language codes with toIntlLocale() from @/i18n/languages before formatting. Use a valid BCP 47 tag for fixed locales.',
        },
      },
      create(context) {
        function checkLocale(node) {
          const callee = node.callee
          const name = callName(callee, context.sourceCode)
          let index = formatterLocaleArguments.get(name)
          if (callee.type === 'MemberExpression') {
            const object = callee.object
            const isIntl =
              object.name === 'Intl' ||
              (object.type === 'MemberExpression' &&
                propertyName(object) === 'Intl')
            if (
              localeMethods.has(name) ||
              (isIntl && intlConstructors.has(name))
            ) {
              index = 0
            }
          }
          if (index === undefined) return
          const locale = node.arguments[index]
          if (hasUnsafeLocale(locale, context.sourceCode)) {
            context.report({ node: locale, messageId: 'unsafe' })
          }
        }
        return { CallExpression: checkLocale, NewExpression: checkLocale }
      },
    },
  },
}
