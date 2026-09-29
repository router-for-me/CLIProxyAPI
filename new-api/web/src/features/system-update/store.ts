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
import { z } from 'zod'
import { create } from 'zustand'
import {
  createJSONStorage,
  persist,
  type StateStorage,
} from 'zustand/middleware'

import { systemReleaseSchema } from './releases'

const updateSnapshotSchema = z
  .object({
    release: systemReleaseSchema.nullable(),
    lastCheckedAt: z.number().finite().nonnegative(),
    lastAttemptAt: z.number().finite().nonnegative(),
    error: z.enum(['network', 'rate-limit', 'timeout', 'payload']).nullable(),
  })
  .refine(
    (snapshot) =>
      snapshot.lastCheckedAt <= snapshot.lastAttemptAt &&
      snapshot.lastAttemptAt <= Date.now()
  )

export type SystemUpdateSnapshot = z.infer<typeof updateSnapshotSchema>

interface SystemUpdateStore {
  snapshot: SystemUpdateSnapshot | null
  setSnapshot: (snapshot: SystemUpdateSnapshot) => void
}

// Checking and notification preferences remain usable when storage is blocked.
const updateStorage: StateStorage = {
  getItem: (key) => {
    try {
      return localStorage.getItem(key)
    } catch {
      return null
    }
  },
  setItem: (key, value) => {
    try {
      localStorage.setItem(key, value)
    } catch {
      /* In-memory state remains usable. */
    }
  },
  removeItem: (key) => {
    try {
      localStorage.removeItem(key)
    } catch {
      /* Storage can be unavailable. */
    }
  },
}

export const useSystemUpdateStore = create<SystemUpdateStore>()(
  persist(
    (set) => ({
      snapshot: null,
      setSnapshot: (snapshot) => set({ snapshot }),
    }),
    {
      name: 'system-update:v1',
      storage: createJSONStorage(() => updateStorage),
      partialize: (state) => ({ snapshot: state.snapshot }),
      merge: (persisted, current) => {
        const parsed = z
          .object({ snapshot: updateSnapshotSchema.nullable() })
          .safeParse(persisted)
        return {
          ...current,
          snapshot: parsed.success ? parsed.data.snapshot : null,
        }
      },
    }
  )
)

const preferencesStorageKey = 'system-update-preferences:v1'
const updatePreferencesSchema = z.object({
  ignoredVersionsByUserId: z.record(
    z.string().regex(/^[1-9]\d*$/),
    z.array(z.string().min(1))
  ),
})

interface SystemUpdatePreferencesStore {
  ignoredVersionsByUserId: Record<string, string[]>
  setVersionIgnored: (userId: number, tag: string, ignored: boolean) => void
}

export const useSystemUpdatePreferencesStore =
  create<SystemUpdatePreferencesStore>()(
    persist(
      (set) => ({
        ignoredVersionsByUserId: {},
        setVersionIgnored: (userId, tag, ignored) => {
          set((state) => {
            const tags = state.ignoredVersionsByUserId[userId] ?? []
            if (tags.includes(tag) === ignored) return state
            const ignoredVersionsByUserId = {
              ...state.ignoredVersionsByUserId,
            }
            const nextTags = ignored
              ? [...tags, tag]
              : tags.filter((value) => value !== tag)
            if (nextTags.length) {
              ignoredVersionsByUserId[userId] = nextTags
            } else {
              delete ignoredVersionsByUserId[userId]
            }
            return { ignoredVersionsByUserId }
          })
        },
      }),
      {
        name: preferencesStorageKey,
        storage: createJSONStorage(() => updateStorage),
        partialize: (state) => ({
          ignoredVersionsByUserId: state.ignoredVersionsByUserId,
        }),
        merge: (persisted, current) => {
          const parsed = updatePreferencesSchema.safeParse(persisted)
          return {
            ...current,
            ignoredVersionsByUserId: parsed.success
              ? parsed.data.ignoredVersionsByUserId
              : {},
          }
        },
      }
    )
  )

let preferenceSubscribers = 0

function handlePreferenceStorage(event: StorageEvent): void {
  if (event.key === preferencesStorageKey || event.key === null) {
    void useSystemUpdatePreferencesStore.persist.rehydrate()
  }
}

// Header and maintenance entries share a single cross-tab storage listener.
export function subscribeSystemUpdatePreferences(
  onChange: () => void
): () => void {
  const unsubscribe = useSystemUpdatePreferencesStore.subscribe(onChange)
  if (preferenceSubscribers++ === 0) {
    window.addEventListener('storage', handlePreferenceStorage)
    void useSystemUpdatePreferencesStore.persist.rehydrate()
  }
  return () => {
    unsubscribe()
    if (--preferenceSubscribers === 0) {
      window.removeEventListener('storage', handlePreferenceStorage)
    }
  }
}
