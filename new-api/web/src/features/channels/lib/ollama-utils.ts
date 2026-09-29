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
import type { Channel } from '../types'

export type PullProgress = {
  status?: string
  completed?: number
  total?: number
  // backend may include extra fields
  [k: string]: unknown
}

export type OllamaModel = {
  id: string
  owned_by?: string
  size?: number
  digest?: string
  modified_at?: string
  details?: unknown
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function getString(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function getNumber(value: unknown): number | undefined {
  return typeof value === 'number' ? value : undefined
}

function parseMaybeJSON(value: unknown) {
  if (!value) return null
  if (typeof value === 'object') return value
  if (typeof value === 'string') {
    try {
      return JSON.parse(value)
    } catch {
      return null
    }
  }
  return null
}

/**
 * Resolve Ollama base URL from channel fields (supports legacy/alternate fields).
 */
export function resolveOllamaBaseUrl(channel: Channel | null) {
  if (!channel) return ''

  const direct =
    typeof channel.base_url === 'string' ? channel.base_url.trim() : ''
  if (direct) return direct

  const alt =
    typeof (channel as unknown as { ollama_base_url?: unknown })
      ?.ollama_base_url === 'string'
      ? String(
          (channel as unknown as { ollama_base_url?: string }).ollama_base_url
        ).trim()
      : ''
  if (alt) return alt

  const parsed = parseMaybeJSON(channel.other_info)
  if (isRecord(parsed)) {
    const baseUrl = getString(parsed.base_url)?.trim()
    if (baseUrl) return baseUrl
    const publicUrl = getString(parsed.public_url)?.trim()
    if (publicUrl) return publicUrl
    const apiUrl = getString(parsed.api_url)?.trim()
    if (apiUrl) return apiUrl
  }

  return ''
}

export function normalizeOllamaModels(items: unknown): OllamaModel[] {
  if (!Array.isArray(items)) return []

  return items
    .map((item) => {
      if (!item) return null

      if (typeof item === 'string') {
        return { id: item, owned_by: 'ollama' } satisfies OllamaModel
      }

      if (isRecord(item)) {
        const candidateId =
          getString(item.id) ||
          getString(item.ID) ||
          getString(item.name) ||
          getString(item.model) ||
          getString(item.Model)
        if (!candidateId) return null

        const metadata = item.metadata ?? item.Metadata
        const normalized: OllamaModel = {
          ...item,
          id: candidateId,
          owned_by:
            getString(item.owned_by) || getString(item.ownedBy) || 'ollama',
        }

        const itemSize = getNumber(item.size)
        if (typeof itemSize === 'number' && !normalized.size) {
          normalized.size = itemSize
        }
        if (isRecord(metadata)) {
          const metaSize = getNumber(metadata.size)
          if (typeof metaSize === 'number' && !normalized.size) {
            normalized.size = metaSize
          }
          const metaDigest = getString(metadata.digest)
          if (!normalized.digest && metaDigest) {
            normalized.digest = metaDigest
          }
          const metaModifiedAt = getString(metadata.modified_at)
          if (!normalized.modified_at && metaModifiedAt) {
            normalized.modified_at = metaModifiedAt
          }
          if (metadata.details && !normalized.details) {
            normalized.details = metadata.details
          }
        }
        return normalized
      }

      return null
    })
    .filter(Boolean) as OllamaModel[]
}

export function formatBytes(bytes?: number) {
  if (typeof bytes !== 'number' || Number.isNaN(bytes)) return '-'
  if (bytes < 1024) return `${bytes} B`
  const kb = bytes / 1024
  if (kb < 1024) return `${kb.toFixed(1)} KB`
  const mb = kb / 1024
  if (mb < 1024) return `${mb.toFixed(1)} MB`
  const gb = mb / 1024
  return `${gb.toFixed(2)} GB`
}
