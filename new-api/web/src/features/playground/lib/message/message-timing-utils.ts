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
import { MESSAGE_ROLES } from '../../constants'
import type { Message } from '../../types'

export function completeAssistantTiming(
  message: Message,
  completedAt: number = Date.now()
): Message {
  if (message.from !== MESSAGE_ROLES.ASSISTANT) {
    return message
  }

  const startedAt = message.startedAt ?? message.createdAt ?? completedAt

  return {
    ...message,
    startedAt,
    completedAt,
    durationMs: Math.max(0, completedAt - startedAt),
  }
}

export function startReasoningTiming(
  message: Message,
  startedAt: number = Date.now()
): NonNullable<Message['reasoning']> {
  return {
    content: message.reasoning?.content ?? '',
    duration: message.reasoning?.duration ?? 0,
    startedAt: message.reasoning?.startedAt ?? startedAt,
    completedAt: message.reasoning?.completedAt,
    durationMs: message.reasoning?.durationMs,
  }
}

export function completeReasoningTiming(
  message: Message,
  completedAt: number = Date.now()
): Message {
  if (!message.reasoning || message.reasoning.durationMs !== undefined) {
    return message
  }

  const startedAt =
    message.reasoning.startedAt ?? message.startedAt ?? completedAt
  const durationMs = Math.max(0, completedAt - startedAt)

  return {
    ...message,
    reasoning: {
      ...message.reasoning,
      startedAt,
      completedAt,
      durationMs,
      duration: Math.ceil(durationMs / 1000),
    },
  }
}
