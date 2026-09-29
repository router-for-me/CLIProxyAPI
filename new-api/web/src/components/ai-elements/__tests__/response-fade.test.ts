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
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import {
  beginRun,
  classifyValue,
  createFadeState,
  endRun,
  FADE_DURATION_MS,
  FADE_HYDRATION_THRESHOLD,
  FADE_STAGGER_MAX_MS,
  FADE_STAGGER_MS,
  splitWords,
  stageRun,
} from '../response-fade'

describe('splitWords', () => {
  test('round-trips ASCII words with trailing whitespace', () => {
    const value = 'Hello world,  stream.\n'
    expect(splitWords(value).join('')).toBe(value)
  })

  test('keeps leading whitespace as its own part', () => {
    expect(splitWords('  hi')).toEqual(['  ', 'hi'])
  })

  test('segments CJK without spaces via Intl.Segmenter', () => {
    const value = '你好世界'
    const parts = splitWords(value)
    expect(parts.join('')).toBe(value)
    expect(parts.length).toBeGreaterThan(1)
  })
})

describe('classifyValue', () => {
  beforeEach(() => {
    vi.spyOn(performance, 'now').mockReturnValue(1000)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  test('animates only newly appended words with capped stagger', () => {
    const state = createFadeState()
    const first = beginRun(state)
    const firstSegments = classifyValue(first, 'one two ')
    endRun(first)

    expect(firstSegments.filter((s) => s.animated)).toHaveLength(2)
    expect(firstSegments[0]?.delay).toBe(0)
    expect(firstSegments[1]?.delay).toBe(FADE_STAGGER_MS)

    vi.spyOn(performance, 'now').mockReturnValue(
      1000 + FADE_DURATION_MS + FADE_STAGGER_MS + 1
    )
    const second = beginRun(state)
    const secondSegments = classifyValue(second, 'one two three four')
    endRun(second)

    const animated = secondSegments.filter((s) => s.animated)
    expect(animated.map((s) => s.value.trim())).toEqual(['three', 'four'])
    expect(animated[0]?.delay).toBe(0)
    expect(animated[1]?.delay).toBe(FADE_STAGGER_MS)
  })

  test('caps stagger delay at FADE_STAGGER_MAX_MS', () => {
    const state = createFadeState()
    const run = beginRun(state)
    const words = Array.from({ length: 20 }, (_, i) => `w${i}`).join(' ')
    const segments = classifyValue(run, words)
    endRun(run)

    const delays = segments.filter((s) => s.animated).map((s) => s.delay)
    expect(Math.max(...delays)).toBe(FADE_STAGGER_MAX_MS)
  })

  test('replays identical delay while still inside the animation window', () => {
    const state = createFadeState()
    const first = beginRun(state)
    classifyValue(first, 'hello ')
    endRun(first)

    vi.spyOn(performance, 'now').mockReturnValue(1000 + FADE_DURATION_MS / 2)
    const second = beginRun(state)
    const segments = classifyValue(second, 'hello world')
    endRun(second)

    expect(segments[0]).toMatchObject({
      animated: true,
      delay: 0,
      start: 0,
      value: 'hello ',
    })
    expect(segments[1]).toMatchObject({
      animated: true,
      start: 6,
      value: 'world',
    })
  })

  test('keeps the same start offset when the head word grows', () => {
    const state = createFadeState()
    const first = beginRun(state)
    const head = classifyValue(first, 'hel')
    endRun(first)
    expect(head[0]?.start).toBe(0)

    vi.spyOn(performance, 'now').mockReturnValue(1050)
    const second = beginRun(state)
    const grown = classifyValue(second, 'hello')
    endRun(second)

    expect(grown[0]?.start).toBe(0)
    expect(grown[0]?.animated).toBe(true)
    expect(grown[0]?.value).toBe('hello')
  })

  test('does not animate whitespace-only parts', () => {
    const state = createFadeState()
    const run = beginRun(state)
    const segments = classifyValue(run, '  \n')
    endRun(run)

    expect(segments.every((s) => !s.animated)).toBe(true)
  })

  test('suppresses animation on the hydration baseline', () => {
    const state = createFadeState()
    const longText = 'a'.repeat(FADE_HYDRATION_THRESHOLD + 1)
    const run = beginRun(state, true)
    const segments = classifyValue(run, longText)
    endRun(run)

    expect(segments.every((s) => !s.animated)).toBe(true)
    expect(state.prevCount).toBe(longText.length)
  })

  test('stops replaying animation after the window expires', () => {
    const state = createFadeState()
    const first = beginRun(state)
    classifyValue(first, 'done ')
    endRun(first)

    vi.spyOn(performance, 'now').mockReturnValue(
      1000 + FADE_DURATION_MS + 1
    )
    const second = beginRun(state)
    const segments = classifyValue(second, 'done next')
    endRun(second)

    expect(segments[0]).toMatchObject({ animated: false, value: 'done ' })
    expect(segments[1]).toMatchObject({ animated: true, value: 'next' })
    expect(state.active.has(0)).toBe(false)
  })

  test('abandoned staged runs leave committed state untouched', () => {
    const state = createFadeState()
    const first = beginRun(state)
    classifyValue(first, 'keep ')
    endRun(first)
    expect(state.prevCount).toBe(5)

    const abandoned = beginRun(state)
    classifyValue(abandoned, 'keep extra')
    stageRun(abandoned)
    // Never commit — simulate React discarding the render
    state.pending = null

    expect(state.prevCount).toBe(5)
    expect(state.active.size).toBe(1)
  })
})
