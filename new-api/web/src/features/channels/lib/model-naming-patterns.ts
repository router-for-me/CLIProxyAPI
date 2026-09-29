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
  deriveModelMappingPairs,
  type ModelMappingPair,
  type ModelMappingRule,
} from './model-mapping-rules'

/** A naming rule detected in an upstream model list, with the models it covers. */
export type ModelNamingSuggestion = {
  id: string
  rule: ModelMappingRule
  models: string[]
}

const MAX_SUGGESTIONS = 3
/** Suffixes relay upstreams commonly append to otherwise standard names. */
const RELAY_SUFFIXES = ['-all', '-latest', '-ca']
const DATE_SUFFIX = /-\d{4}-\d{2}-\d{2}$/
/** Unverified prefix groups need this many models before they are suggested. */
const MIN_PREFIX_MATCHES = 2

/**
 * Detects how an upstream list deviates from the platform's known model names:
 * relay suffixes, date suffixes, vendor prefixes and dotted versions. Known
 * names are never candidates, so a rule that would rename a real model is not
 * suggested just because a shorter real name exists.
 */
export function detectModelNamingPatterns(
  models: readonly string[],
  knownModels: readonly string[]
): ModelNamingSuggestion[] {
  const known = new Set(
    knownModels.map((model) => model.trim()).filter(Boolean)
  )
  const candidates = [
    ...new Set(
      models
        .map((model) => model.trim())
        .filter((model) => model && !known.has(model))
    ),
  ]
  const suggestions: Array<ModelNamingSuggestion & { verified: number }> = []

  for (const suffix of RELAY_SUFFIXES) {
    const matched = candidates.filter(
      (model) => model.endsWith(suffix) && model.length > suffix.length
    )
    if (matched.length === 0) continue
    suggestions.push({
      id: `strip-suffix:${suffix}`,
      rule: { type: 'strip-suffix', values: suffix },
      models: matched,
      verified: matched.filter((model) =>
        known.has(model.slice(0, -suffix.length))
      ).length,
    })
  }

  const dateSuffixes = new Set(
    candidates.map((model) => DATE_SUFFIX.exec(model)?.[0]).filter(Boolean)
  )
  for (const suffix of dateSuffixes) {
    if (!suffix) continue
    const matched = candidates.filter(
      (model) => model.endsWith(suffix) && model.length > suffix.length
    )
    suggestions.push({
      id: `strip-suffix:${suffix}`,
      rule: { type: 'strip-suffix', values: suffix },
      models: matched,
      verified: matched.filter((model) =>
        known.has(model.slice(0, -suffix.length))
      ).length,
    })
  }

  const prefixes = new Set(
    candidates
      .map((model) => {
        const slash = model.indexOf('/')
        return slash > 0 && slash < model.length - 1
          ? model.slice(0, slash + 1)
          : ''
      })
      .filter(Boolean)
  )
  for (const prefix of prefixes) {
    const matched = candidates.filter((model) => model.startsWith(prefix))
    const verified = matched.filter((model) =>
      known.has(model.slice(prefix.length))
    ).length
    if (verified === 0 && matched.length < MIN_PREFIX_MATCHES) continue
    suggestions.push({
      id: `strip-prefix:${prefix}`,
      rule: { type: 'strip-prefix', values: prefix },
      models: matched,
      verified,
    })
  }

  const dotted = candidates.filter(
    (model) => model.includes('.') && known.has(model.replaceAll('.', '-'))
  )
  if (dotted.length > 0) {
    suggestions.push({
      id: 'replace:.:-',
      rule: { type: 'replace', find: '.', replaceWith: '-' },
      models: dotted,
      verified: dotted.length,
    })
  }

  return suggestions
    .sort(
      (a, b) =>
        b.models.length - a.models.length ||
        b.verified - a.verified ||
        a.id.localeCompare(b.id)
    )
    .slice(0, MAX_SUGGESTIONS)
    .map(({ id, rule, models: covered }) => ({ id, rule, models: covered }))
}

/**
 * Returns the request-to-upstream pair a suggestion produces for one model,
 * but only when the derived request name is a known platform model. That is
 * the bar for applying an alias without asking the user first.
 */
export function findVerifiedAlias(
  model: string,
  suggestions: readonly ModelNamingSuggestion[],
  knownModels: readonly string[]
): ModelMappingPair | null {
  const known = new Set(knownModels)
  for (const suggestion of suggestions) {
    if (!suggestion.models.includes(model)) continue
    const pair = deriveModelMappingPairs([model], 'upstream', suggestion.rule)
      .pairs[0]
    if (pair && known.has(pair.from)) return pair
  }
  return null
}
