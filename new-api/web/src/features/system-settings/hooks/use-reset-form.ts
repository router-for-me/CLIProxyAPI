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
import { useEffect, useRef } from 'react'
import type { DefaultValues, FieldValues, UseFormReturn } from 'react-hook-form'

/**
 * Reset a react-hook-form instance whenever the provided default values change.
 * Guards against naively resetting on every render by tracking the last
 * serialized snapshot of the defaults.
 */
export function useResetForm<TFieldValues extends FieldValues>(
  form: UseFormReturn<TFieldValues>,
  values: DefaultValues<TFieldValues> | undefined
) {
  const lastSerializedDefaults = useRef<string | null>(null)

  useEffect(() => {
    if (!values) return

    const serializedDefaults = JSON.stringify(values)
    if (serializedDefaults === lastSerializedDefaults.current) {
      return
    }

    form.reset(values)
    lastSerializedDefaults.current = serializedDefaults
  }, [values, form])
}
