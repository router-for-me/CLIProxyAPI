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

type MessageEditorState = {
  canSave: boolean
  hasChanged: boolean
  showSaveAndSubmit: boolean
}

export function getMessageEditorState(
  message: Message,
  editText: string,
  originalText: string
): MessageEditorState {
  const hasText = editText.trim().length > 0
  const hasChanged = editText !== originalText

  return {
    canSave: hasText && hasChanged,
    hasChanged,
    showSaveAndSubmit: message.from === MESSAGE_ROLES.USER,
  }
}
