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
import * as React from 'react'

/**
 * Local helper for components that can be either controlled or uncontrolled.
 *
 * Mirrors the original signature: pass either a controlled `prop` (with
 * optional `onChange` callback) or an uncontrolled `defaultProp`. Returns a
 * `[value, setValue]` tuple where `setValue` accepts either a new value or
 * an updater function, just like `useState`.
 *
 * The return tuple's value type is non-`undefined` whenever `defaultProp`
 * is provided, keeping existing call sites well-typed.
 */
type SetStateFn<T> = (prevState?: T) => T

type Setter<T> = (next: T | undefined | SetStateFn<T | undefined>) => void

export function useControllableState<T>(params: {
  prop?: T | undefined
  defaultProp: T
  onChange?: (state: T) => void
}): [T, Setter<T>]
export function useControllableState<T>(params: {
  prop?: T | undefined
  defaultProp?: T | undefined
  onChange?: (state: T) => void
}): [T | undefined, Setter<T>]
export function useControllableState<T>({
  prop,
  defaultProp,
  onChange,
}: {
  prop?: T | undefined
  defaultProp?: T | undefined
  onChange?: (state: T) => void
}): [T | undefined, Setter<T>] {
  const [uncontrolledProp, setUncontrolledProp] = React.useState<T | undefined>(
    defaultProp
  )
  const isControlled = prop !== undefined
  const value = isControlled ? prop : uncontrolledProp
  const handleChangeRef = React.useRef(onChange)

  React.useEffect(() => {
    handleChangeRef.current = onChange
  })

  const setValue = React.useCallback<Setter<T>>(
    (next) => {
      if (isControlled) {
        const nextValue =
          typeof next === 'function'
            ? (next as SetStateFn<T | undefined>)(prop)
            : next
        if (nextValue !== prop) {
          handleChangeRef.current?.(nextValue as T)
        }
      } else {
        setUncontrolledProp(
          next as T | undefined | ((prev: T | undefined) => T | undefined)
        )
      }
    },
    [isControlled, prop]
  )

  return [value, setValue]
}
