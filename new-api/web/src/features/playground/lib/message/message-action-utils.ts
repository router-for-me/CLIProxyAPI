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
import { MESSAGE_ROLES, MESSAGE_STATUS } from '../../constants'
import type { Message } from '../../types'
import { getMessageContent, hasMessageContent } from './message-utils'

type MessageActionState = {
  content: string
  hasContent: boolean
  isAssistant: boolean
  isLoading: boolean
  isUser: boolean
}

export function getMessageActionState(message: Message): MessageActionState {
  return {
    content: getMessageContent(message),
    hasContent: hasMessageContent(message),
    isAssistant: message.from === MESSAGE_ROLES.ASSISTANT,
    isUser: message.from === MESSAGE_ROLES.USER,
    isLoading:
      message.status === MESSAGE_STATUS.LOADING ||
      message.status === MESSAGE_STATUS.STREAMING,
  }
}

export function getMessageActionsVisibilityClass(
  alwaysVisible: boolean
): string {
  return alwaysVisible
    ? 'opacity-100'
    : 'opacity-0 group-hover:opacity-100 max-md:opacity-100'
}
