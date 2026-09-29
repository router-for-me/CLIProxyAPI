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
import { selectLatestRelease, type SystemRelease } from './releases'

export type UpdateCheckErrorCode =
  | 'network'
  | 'rate-limit'
  | 'timeout'
  | 'payload'

export class UpdateCheckError extends Error {
  constructor(public readonly code: UpdateCheckErrorCode) {
    super(code)
    this.name = 'UpdateCheckError'
  }
}

export async function fetchLatestSystemRelease(
  signal: AbortSignal
): Promise<SystemRelease | null> {
  const controller = new AbortController()
  const cancel = () => controller.abort()
  signal.addEventListener('abort', cancel, { once: true })
  if (signal.aborted) controller.abort()
  const timeout = setTimeout(cancel, 10_000)

  try {
    const response = await fetch(
      'https://api.github.com/repos/QuantumNous/new-api/releases?per_page=100',
      {
        credentials: 'omit',
        headers: { Accept: 'application/vnd.github+json' },
        signal: controller.signal,
      }
    )
    if (response.status === 403 || response.status === 429) {
      throw new UpdateCheckError('rate-limit')
    }
    if (!response.ok) throw new UpdateCheckError('network')

    try {
      return selectLatestRelease(await response.json())
    } catch (error) {
      if (controller.signal.aborted) throw error
      throw new UpdateCheckError('payload')
    }
  } catch (error) {
    if (signal.aborted) throw error
    if (controller.signal.aborted) throw new UpdateCheckError('timeout')
    if (error instanceof UpdateCheckError) throw error
    throw new UpdateCheckError('network')
  } finally {
    clearTimeout(timeout)
    signal.removeEventListener('abort', cancel)
  }
}
