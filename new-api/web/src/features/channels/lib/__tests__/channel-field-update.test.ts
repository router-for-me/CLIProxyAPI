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
  CHANNEL_FIELD_UPDATE_DELAY_MS,
  createChannelFieldUpdateScheduler,
} from '../channel-field-update'

function createFakeTimers() {
  const pending = new Map<number, () => void>()
  let nextId = 1

  return {
    timers: {
      setTimeout: (callback: () => void, delay: number) => {
        expect(delay).toBe(CHANNEL_FIELD_UPDATE_DELAY_MS)
        const id = nextId++
        pending.set(id, callback)
        return id
      },
      clearTimeout: (id: number) => {
        pending.delete(id)
      },
    },
    fireAll() {
      const callbacks = [...pending.values()]
      pending.clear()
      for (const callback of callbacks) callback()
    },
    get pendingCount() {
      return pending.size
    },
  }
}

describe('channel field update scheduler', () => {
  test('coalesces rapid schedules into one update with the latest value', () => {
    const fake = createFakeTimers()
    const updates: number[] = []
    const scheduler = createChannelFieldUpdateScheduler(
      (value) => updates.push(value),
      fake.timers
    )

    scheduler.schedule(1)
    scheduler.schedule(2)
    scheduler.schedule(3)
    expect(updates).toEqual([])
    expect(fake.pendingCount).toBe(1)

    fake.fireAll()
    expect(updates).toEqual([3])
  })

  test('flush commits the pending value immediately and cancels the timer', () => {
    const fake = createFakeTimers()
    const updates: number[] = []
    const scheduler = createChannelFieldUpdateScheduler(
      (value) => updates.push(value),
      fake.timers
    )

    scheduler.schedule(7)
    scheduler.flush()
    expect(updates).toEqual([7])
    expect(fake.pendingCount).toBe(0)

    fake.fireAll()
    expect(updates).toEqual([7])
  })

  test('flush without a pending value does nothing', () => {
    const fake = createFakeTimers()
    const updates: number[] = []
    const scheduler = createChannelFieldUpdateScheduler(
      (value) => updates.push(value),
      fake.timers
    )

    scheduler.flush()
    scheduler.schedule(5)
    scheduler.flush()
    scheduler.flush()
    expect(updates).toEqual([5])
  })

  test('preserves a pending value of 0', () => {
    const fake = createFakeTimers()
    const updates: number[] = []
    const scheduler = createChannelFieldUpdateScheduler(
      (value) => updates.push(value),
      fake.timers
    )

    scheduler.schedule(0)
    scheduler.flush()
    expect(updates).toEqual([0])
  })
})
