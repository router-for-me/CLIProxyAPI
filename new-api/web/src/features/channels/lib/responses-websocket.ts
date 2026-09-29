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
import { CHANNEL_TYPE_NEW_API, CHANNEL_TYPE_SUB2API } from '../constants'
import { CHANNEL_TYPE_ADVANCED_CUSTOM } from './advanced-custom'

/**
 * Channel types whose upstream can carry the Responses WebSocket protocol.
 * Mirrors the backend FilterResponsesWebSocket allow list; the per-channel
 * toggle still decides whether a channel is actually used.
 */
const RESPONSES_WEBSOCKET_CHANNEL_TYPES: ReadonlySet<number> = new Set([
  1,
  57,
  CHANNEL_TYPE_ADVANCED_CUSTOM,
  CHANNEL_TYPE_SUB2API,
  CHANNEL_TYPE_NEW_API,
])

export function supportsResponsesWebSocket(channelType: number): boolean {
  return RESPONSES_WEBSOCKET_CHANNEL_TYPES.has(channelType)
}
