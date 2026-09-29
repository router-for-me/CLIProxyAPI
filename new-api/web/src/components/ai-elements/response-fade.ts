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

/** Must match the animation duration on `[data-stream-fade]` in styles/index.css */
export const FADE_DURATION_MS = 250
export const FADE_STAGGER_MS = 25
export const FADE_STAGGER_MAX_MS = 250
/**
 * Text already longer than this when animation starts is hydrated/resumed
 * content (reconnected stream, conversation switch), not a fresh delta —
 * that content becomes the baseline instead of re-fading.
 */
export const FADE_HYDRATION_THRESHOLD = 120

type FadeEntry = { at: number; delay: number }

type PendingRun = {
  prevCount: number
  additions: Map<number, FadeEntry>
  now: number
}

export type FadeState = {
  /** Total characters classified during the last committed run */
  prevCount: number
  /** Parts still mid-animation, keyed by start offset */
  active: Map<number, FadeEntry>
  /** True until the first run commits */
  firstRun: boolean
  /** Staged result of the latest render; published on commit */
  pending: PendingRun | null
}

export type FadeRun = {
  state: FadeState
  now: number
  count: number
  newIndex: number
  /** Baseline mode: classify everything as already seen */
  suppress: boolean
  additions: Map<number, FadeEntry>
}

export type FadeSegment = {
  start: number
  value: string
  animated: boolean
  delay: number
}

const WORD_REGEX = /\S+\s*/g
const NON_WHITESPACE_REGEX = /\S/
/**
 * Scripts without word-delimiting spaces: Thai, Lao, Myanmar, Khmer,
 * Tibetan, CJK ideographs/kana, Hangul, and CJK compatibility ideographs.
 */
const SPACELESS_REGEX =
  /[\u0E00-\u0EFF\u0F00-\u0FFF\u1000-\u109F\u1780-\u17FF\u2E80-\u9FFF\uAC00-\uD7AF\uF900-\uFAFF]/

let wordSegmenter: Intl.Segmenter | null | undefined

function getWordSegmenter(): Intl.Segmenter | null {
  if (wordSegmenter === undefined) {
    wordSegmenter =
      typeof Intl !== 'undefined' && typeof Intl.Segmenter === 'function'
        ? new Intl.Segmenter(undefined, { granularity: 'word' })
        : null
  }
  return wordSegmenter
}

function pushSegmentedParts(parts: string[], token: string): void {
  const segmenter = getWordSegmenter()
  if (segmenter == null) {
    parts.push(token)
    return
  }
  const trailing = /\s+$/.exec(token)
  const word = trailing == null ? token : token.slice(0, trailing.index)
  for (const segment of segmenter.segment(word)) {
    parts.push(segment.segment)
  }
  if (trailing != null) {
    parts.push(trailing[0])
  }
}

/**
 * Splits text into word parts (non-whitespace run plus trailing whitespace).
 * Spaceless scripts are further split via Intl.Segmenter.
 * Concatenating the result always reproduces the input exactly.
 */
export function splitWords(value: string): string[] {
  const parts: string[] = []
  WORD_REGEX.lastIndex = 0
  let index = 0
  let match: RegExpExecArray | null
  while ((match = WORD_REGEX.exec(value)) !== null) {
    if (match.index > index) {
      parts.push(value.slice(index, match.index))
    }
    const token = match[0]
    if (SPACELESS_REGEX.test(token)) {
      pushSegmentedParts(parts, token)
    } else {
      parts.push(token)
    }
    index = match.index + token.length
  }
  if (index < value.length) {
    parts.push(value.slice(index))
  }
  return parts
}

export function createFadeState(): FadeState {
  return { prevCount: 0, active: new Map(), firstRun: true, pending: null }
}

export function beginRun(state: FadeState, suppress = false): FadeRun {
  return {
    state,
    now: performance.now(),
    count: 0,
    newIndex: 0,
    suppress,
    additions: new Map(),
  }
}

/**
 * Stages the run's result without publishing. Classification never mutates
 * committed state during render, so abandoned renders leave no trace.
 */
export function stageRun(run: FadeRun): void {
  run.state.pending = {
    prevCount: run.count,
    additions: run.additions,
    now: run.now,
  }
}

/** Publishes the staged run: baseline offset, new animations, pruned entries. */
export function commitRun(state: FadeState): void {
  const pending = state.pending
  if (pending == null) {
    return
  }
  state.pending = null
  state.firstRun = false
  state.prevCount = pending.prevCount
  for (const [start, entry] of pending.additions) {
    state.active.set(start, entry)
  }
  for (const [start, entry] of state.active) {
    if (pending.now - entry.at >= entry.delay + FADE_DURATION_MS) {
      state.active.delete(start)
    }
  }
}

/** Stages and immediately commits — for callers without a commit phase. */
export function endRun(run: FadeRun): void {
  stageRun(run)
  commitRun(run.state)
}

/**
 * Classifies one text value into fade segments, advancing document-order
 * character offset. New parts (start >= prevCount) animate; parts still
 * inside their animation window replay identical props.
 */
export function classifyValue(run: FadeRun, value: string): FadeSegment[] {
  const { state, now } = run
  const segments: FadeSegment[] = []
  for (const part of splitWords(value)) {
    const start = run.count
    run.count += part.length
    if (run.suppress || !NON_WHITESPACE_REGEX.test(part)) {
      segments.push({ start, value: part, animated: false, delay: 0 })
      continue
    }
    if (start >= state.prevCount) {
      const staged = run.additions.get(start)
      const delay =
        staged?.delay ??
        Math.min(run.newIndex * FADE_STAGGER_MS, FADE_STAGGER_MAX_MS)
      run.newIndex += 1
      run.additions.set(start, staged ?? { at: now, delay })
      segments.push({ start, value: part, animated: true, delay })
      continue
    }
    const entry = state.active.get(start)
    if (entry != null && now - entry.at < entry.delay + FADE_DURATION_MS) {
      segments.push({
        start,
        value: part,
        animated: true,
        delay: entry.delay,
      })
      continue
    }
    segments.push({ start, value: part, animated: false, delay: 0 })
  }
  return segments
}
