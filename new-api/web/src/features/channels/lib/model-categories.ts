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

import { resolveModelProvider } from '@/lib/model-provider'

export function getModelCategory(modelName: string): string {
  return resolveModelProvider(modelName)?.name ?? 'Other'
}

export function categorizeModels(
  models: readonly string[]
): Record<string, string[]> {
  const categories: Record<string, string[]> = {}

  for (const model of models) {
    const category = getModelCategory(model)
    categories[category] ??= []
    categories[category].push(model)
  }

  return categories
}
