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
import { TASK_STATUS } from '../constants'
import type {
  AudioClip,
  TaskArtifact,
  TaskArtifactProjection,
  TaskArtifactsResponse,
  TaskLog,
  TaskPluginAuthor,
} from '../types'

const taskArtifactTypes = new Set(['image', 'video', 'audio', 'file'])
const safeArtifactKeyPattern = /^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$/
const taskArtifactContentPathPattern =
  /\/v1\/tasks\/[^/]+\/artifacts\/[^/]+\/content$/
const taskArtifactAccessTokenPattern = /^[A-Za-z0-9_-]{43}$/
const maxTaskArtifacts = 64
export type TaskPreviewMode =
  | 'plugin'
  | 'legacy-suno'
  | 'legacy-video'
  | 'discarded'
  | 'none'

export class TaskArtifactApiError extends Error {
  constructor(
    message: string,
    public code?: string
  ) {
    super(message)
    this.name = 'TaskArtifactApiError'
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function parseContentUrl(value: unknown): string {
  if (typeof value !== 'string') {
    throw new TaskArtifactApiError('invalid_content_url')
  }

  const contentUrl = value.trim()
  if (
    contentUrl.length === 0 ||
    contentUrl !== value ||
    contentUrl.includes('#') ||
    !/^https?:\/\//i.test(contentUrl)
  ) {
    throw new TaskArtifactApiError('invalid_content_url')
  }
  for (const character of contentUrl) {
    const codePoint = character.codePointAt(0) ?? 0
    if (codePoint <= 0x1f || codePoint === 0x7f || character === '\\') {
      throw new TaskArtifactApiError('invalid_content_url')
    }
  }

  try {
    const url = new URL(contentUrl)
    const authorityStart = contentUrl.indexOf('//') + 2
    const pathStart = contentUrl.indexOf('/', authorityStart)
    const authority = contentUrl.slice(
      authorityStart,
      pathStart === -1 ? contentUrl.length : pathStart
    )
    if (
      (url.protocol !== 'https:' && url.protocol !== 'http:') ||
      url.hostname.length === 0 ||
      authority.includes('@') ||
      url.username ||
      url.password ||
      url.hash ||
      !taskArtifactContentPathPattern.test(url.pathname)
    ) {
      throw new TaskArtifactApiError('invalid_content_url')
    }
    const accessToken = url.searchParams.get('access')
    if (
      accessToken == null ||
      !taskArtifactAccessTokenPattern.test(accessToken) ||
      url.search !== `?access=${accessToken}`
    ) {
      throw new TaskArtifactApiError('invalid_content_url')
    }
    return contentUrl
  } catch (error) {
    if (error instanceof TaskArtifactApiError) throw error
    throw new TaskArtifactApiError('invalid_content_url')
  }
}

function parseTaskArtifact(value: unknown): TaskArtifact {
  if (!isRecord(value)) {
    throw new TaskArtifactApiError('invalid_artifact')
  }

  const key = typeof value.key === 'string' ? value.key : ''
  const type = typeof value.type === 'string' ? value.type : ''
  if (
    key !== key.trim() ||
    !safeArtifactKeyPattern.test(key) ||
    !taskArtifactTypes.has(type)
  ) {
    throw new TaskArtifactApiError('invalid_artifact')
  }

  const artifact: TaskArtifact = {
    key,
    type: type as TaskArtifact['type'],
    content_url: parseContentUrl(value.content_url),
  }
  if (typeof value.mime_type === 'string' && value.mime_type.trim()) {
    const mimeType = value.mime_type.trim()
    if (mimeType.length > 255 || /[\r\n]/.test(mimeType)) {
      throw new TaskArtifactApiError('invalid_artifact')
    }
    artifact.mime_type = mimeType
  }
  return artifact
}

// Legacy Suno tasks persisted their clip list as an array or as a JSON string
// holding one. Only clips with an audio URL can be previewed.
export function parseLegacyAudioClips(data: unknown): AudioClip[] {
  let values: unknown[] = []
  if (Array.isArray(data)) {
    values = data
  } else if (typeof data === 'string') {
    try {
      const parsed = JSON.parse(data)
      values = Array.isArray(parsed) ? parsed : []
    } catch {
      return []
    }
  }
  return values
    .filter(
      (value): value is AudioClip =>
        isRecord(value) && typeof value.audio_url === 'string'
    )
    .slice(0, maxTaskArtifacts)
}

export function parseTaskArtifactsResponse(
  response: TaskArtifactsResponse
): TaskArtifactProjection {
  if (!response.success) {
    throw new TaskArtifactApiError(
      response.message || 'artifact_projection_failed',
      response.code
    )
  }

  const rawArtifacts = response.data?.artifacts
  if (rawArtifacts != null && !Array.isArray(rawArtifacts)) {
    throw new TaskArtifactApiError('invalid_artifact_response')
  }
  const artifactValues = rawArtifacts ?? []
  if (artifactValues.length > maxTaskArtifacts) {
    throw new TaskArtifactApiError('invalid_artifact_response')
  }

  const artifacts = artifactValues.map(parseTaskArtifact)
  const keys = new Set<string>()
  for (const artifact of artifacts) {
    if (keys.has(artifact.key)) {
      throw new TaskArtifactApiError('duplicate_artifact_key')
    }
    keys.add(artifact.key)
  }
  const projection: TaskArtifactProjection = { artifacts }
  if (response.data?.legacy_content_url != null) {
    projection.legacyContentUrl = parseContentUrl(
      response.data.legacy_content_url
    )
  }
  if (response.data?.legacy_audio_clips != null) {
    projection.legacyAudioClips = parseLegacyAudioClips(
      response.data.legacy_audio_clips
    )
  }
  return projection
}

export function shouldLoadTaskArtifacts(
  log: TaskLog,
  dialogOpen: boolean
): boolean {
  return (
    dialogOpen &&
    log.status === TASK_STATUS.SUCCESS &&
    log.result_discarded !== true
  )
}

export function resolveTaskPreviewMode(
  log: TaskLog,
  hasProjectedArtifacts = false
): TaskPreviewMode {
  if (log.status !== TASK_STATUS.SUCCESS) return 'none'
  // A synchronous result was returned inline and never persisted; there is
  // nothing to project, so the viewer explains that instead of opening.
  if (log.result_discarded === true) return 'discarded'
  if (hasProjectedArtifacts) return 'plugin'
  if (log.admin_info?.task_plugin) return 'plugin'
  if (log.platform === 'suno') return 'legacy-suno'
  if (log.legacy_video_available) return 'legacy-video'
  return 'plugin'
}

export function getSafePluginAuthorUrl(
  author?: TaskPluginAuthor
): string | undefined {
  if (!author?.url) return undefined
  try {
    const url = new URL(author.url)
    if (url.protocol !== 'https:' && url.protocol !== 'http:') return undefined
    return url.toString()
  } catch {
    return undefined
  }
}
