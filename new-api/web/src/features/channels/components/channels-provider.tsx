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
/* eslint-disable react-refresh/only-export-components */
import { useQueryClient } from '@tanstack/react-query'
import React, {
  createContext,
  useContext,
  useState,
  useCallback,
  useMemo,
} from 'react'

import { useChannelUpstreamUpdates } from '../hooks/use-channel-upstream-updates'
import { channelsQueryKeys } from '../lib'
import type { Channel } from '../types'

// ============================================================================
// Types
// ============================================================================

type DialogType =
  | 'create-channel'
  | 'update-channel'
  | 'test-channel'
  | 'balance-query'
  | 'fetch-models'
  | 'ollama-models'
  | 'inference-status'
  | 'multi-key-manage'
  | 'tag-batch-edit'
  | 'edit-tag'
  | 'copy-channel'
  | null

type UpstreamUpdateState = ReturnType<typeof useChannelUpstreamUpdates>

type ChannelsContextType = {
  open: DialogType
  setOpen: (open: DialogType) => void
  currentRow: Channel | null
  setCurrentRow: (row: Channel | null) => void
  currentTag: string | null
  setCurrentTag: (tag: string | null) => void
  enableTagMode: boolean
  setEnableTagMode: (enabled: boolean) => void
  idSort: boolean
  setIdSort: (enabled: boolean) => void
  batchMode: boolean
  setBatchMode: (enabled: boolean) => void
  sensitiveVisible: boolean
  setSensitiveVisible: (visible: boolean) => void
  upstream: UpstreamUpdateState
}

// ============================================================================
// Context
// ============================================================================

const ChannelsContext = createContext<ChannelsContextType | undefined>(
  undefined
)

// ============================================================================
// Provider
// ============================================================================

export function ChannelsProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState<DialogType>(null)
  const [currentRow, setCurrentRow] = useState<Channel | null>(null)
  const [currentTag, setCurrentTag] = useState<string | null>(null)
  const [enableTagMode, setEnableTagMode] = useState(() => {
    return localStorage.getItem('enable-tag-mode') === 'true'
  })
  const [idSort, setIdSort] = useState(() => {
    return localStorage.getItem('channels-id-sort') === 'true'
  })
  const [batchMode, setBatchMode] = useState(false)
  const [sensitiveVisible, setSensitiveVisible] = useState(true)

  const queryClient = useQueryClient()
  const refreshChannels = useCallback(async () => {
    await queryClient.invalidateQueries({ queryKey: channelsQueryKeys.all })
  }, [queryClient])
  const upstream = useChannelUpstreamUpdates(refreshChannels)

  // useState setters are stable, so the context value only needs to change when
  // an actual state value changes. Memoizing avoids handing every consumer
  // (including all channel cards/cells) a brand-new object on each render.
  const value = useMemo<ChannelsContextType>(
    () => ({
      open,
      setOpen,
      currentRow,
      setCurrentRow,
      currentTag,
      setCurrentTag,
      enableTagMode,
      setEnableTagMode,
      idSort,
      setIdSort,
      batchMode,
      setBatchMode,
      sensitiveVisible,
      setSensitiveVisible,
      upstream,
    }),
    [
      open,
      currentRow,
      currentTag,
      enableTagMode,
      idSort,
      batchMode,
      sensitiveVisible,
      upstream,
    ]
  )

  return (
    <ChannelsContext.Provider value={value}>
      {children}
    </ChannelsContext.Provider>
  )
}

// ============================================================================
// Hook
// ============================================================================

export function useChannels() {
  const context = useContext(ChannelsContext)
  if (!context) {
    throw new Error('useChannels must be used within ChannelsProvider')
  }
  return context
}
