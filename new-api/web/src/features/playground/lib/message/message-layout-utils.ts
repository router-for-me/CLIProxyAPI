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
import type { Message, PlaygroundMessageLayoutMode } from '../../types'

export type MessageAlignment = 'left' | 'right'

export function getMessageAlignment(
  message: Message,
  layoutMode: PlaygroundMessageLayoutMode
): MessageAlignment {
  if (layoutMode === 'left') {
    return 'left'
  }

  return message.from === MESSAGE_ROLES.USER ? 'right' : 'left'
}

export function getMessageAlignmentClass(alignment: MessageAlignment): string {
  return alignment === 'right'
    ? 'items-end text-right'
    : 'items-start text-left'
}
