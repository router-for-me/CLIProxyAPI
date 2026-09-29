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
import { useRef, useState, useCallback, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { api, type ApiRequestConfig } from '@/lib/api'
import { handleServerError } from '@/lib/handle-server-error'
import {
  createServerError,
  getServerErrorMessage,
} from '@/lib/server-error-message'

import { normalizeModelList } from '../lib/upstream-update-utils'

const upstreamUpdateRequestConfig = {
  skipBusinessError: true,
  skipErrorHandler: true,
} satisfies ApiRequestConfig

type UpstreamUpdateChannel = { id: number; name?: string }

type UpstreamUpdateResult = {
  addedModels: string[]
  removedModels: string[]
  remainingModels: string[]
  remainingRemoveModels: string[]
}

type UpstreamUpdateResponse<T> = {
  success: boolean
  message?: string
  data?: T
}

export function useChannelUpstreamUpdates(refresh: () => Promise<void>) {
  const { t } = useTranslation()

  const [showModal, setShowModal] = useState(false)
  const [channel, setChannel] = useState<UpstreamUpdateChannel | null>(null)
  const [addModels, setAddModels] = useState<string[]>([])
  const [removeModels, setRemoveModels] = useState<string[]>([])
  const [preferredTab, setPreferredTab] = useState<'add' | 'remove'>('add')
  const [applyLoading, setApplyLoading] = useState(false)
  const [detectLoading, setDetectLoading] = useState(false)
  const [detectError, setDetectError] = useState<string | null>(null)
  const [applyError, setApplyError] = useState<string | null>(null)
  const [result, setResult] = useState<UpstreamUpdateResult | null>(null)
  const [previewVersion, setPreviewVersion] = useState(0)
  const [detectAllLoading, setDetectAllLoading] = useState(false)
  const [applyAllLoading, setApplyAllLoading] = useState(false)

  const applyRef = useRef(false)
  const previewRequestRef = useRef(0)
  const detectAllRef = useRef(false)
  const applyAllRef = useRef(false)

  const openModal = useCallback(
    (
      record: UpstreamUpdateChannel | null,
      pendingAdd: string[] = [],
      pendingRemove: string[] = [],
      tab: 'add' | 'remove' = 'add'
    ) => {
      if (applyRef.current) return
      const normAdd = normalizeModelList(pendingAdd)
      const normRemove = normalizeModelList(pendingRemove)
      if (!record?.id) return
      previewRequestRef.current += 1
      setPreviewVersion((version) => version + 1)
      setDetectLoading(false)
      setDetectError(null)
      setApplyError(null)
      setResult(null)
      setChannel(record)
      setAddModels(normAdd)
      setRemoveModels(normRemove)
      setPreferredTab(tab)
      setShowModal(true)
    },
    []
  )

  const closeModal = useCallback(() => {
    if (applyRef.current) return
    previewRequestRef.current += 1
    setDetectLoading(false)
    setDetectError(null)
    setApplyError(null)
    setResult(null)
    setShowModal(false)
    setChannel(null)
    setAddModels([])
    setRemoveModels([])
    setPreferredTab('add')
  }, [])

  const applyUpdates = useCallback(
    async ({
      addModels: selectedAdd = [],
      removeModels: selectedRemove = [],
    }: {
      addModels?: string[]
      removeModels?: string[]
    } = {}) => {
      if (
        applyRef.current ||
        detectLoading ||
        detectError ||
        applyError ||
        result ||
        !channel?.id
      ) {
        return
      }
      const normSelectedAdd = normalizeModelList(selectedAdd).filter((model) =>
        addModels.includes(model)
      )
      const normSelectedRemove = normalizeModelList(selectedRemove).filter(
        (model) => removeModels.includes(model)
      )
      if (!normSelectedAdd.length && !normSelectedRemove.length) return
      applyRef.current = true
      setApplyLoading(true)
      try {
        const res = await api.post<
          UpstreamUpdateResponse<{
            added_models: string[] | null
            removed_models: string[] | null
            remaining_models: string[] | null
            remaining_remove_models: string[] | null
          }>
        >(
          '/api/channel/upstream_updates/apply',
          {
            id: channel.id,
            add_models: normSelectedAdd,
            ignore_models: [],
            remove_models: normSelectedRemove,
          },
          upstreamUpdateRequestConfig
        )
        if (!res.data?.success || !res.data.data) {
          throw createServerError(res.data, t('Operation failed'))
        }
        const data = res.data.data
        setResult({
          addedModels: normalizeModelList(data.added_models ?? []),
          removedModels: normalizeModelList(data.removed_models ?? []),
          remainingModels: normalizeModelList(data.remaining_models ?? []),
          remainingRemoveModels: normalizeModelList(
            data.remaining_remove_models ?? []
          ),
        })
      } catch (error: unknown) {
        setApplyError(getServerErrorMessage(error, t('Operation failed')))
        handleServerError(error, t('Operation failed'))
      } finally {
        applyRef.current = false
        setApplyLoading(false)
      }
      // A list refresh failure must not turn a successful update into a failed one.
      void refresh().catch((error: unknown) => handleServerError(error))
    },
    [
      channel,
      addModels,
      removeModels,
      detectLoading,
      detectError,
      applyError,
      result,
      refresh,
      t,
    ]
  )

  const applyAllUpdates = useCallback(async () => {
    if (applyAllRef.current) return
    applyAllRef.current = true
    setApplyAllLoading(true)
    try {
      const res = await api.post(
        '/api/channel/upstream_updates/apply_all',
        {},
        upstreamUpdateRequestConfig
      )
      const { success, data } = res.data || {}
      if (!success) {
        handleServerError(res.data, t('Batch processing failed'))
        return
      }

      toast.success(
        t(
          'Batch upstream model updates applied: {{channels}} channels, {{added}} added, {{removed}} removed, {{fails}} failed',
          {
            channels: data?.processed_channels || 0,
            added: data?.added_models || 0,
            removed: data?.removed_models || 0,
            fails: (data?.failed_channel_ids || []).length,
          }
        )
      )
      await refresh()
    } catch (e: unknown) {
      handleServerError(e, t('Batch processing failed'))
    } finally {
      applyAllRef.current = false
      setApplyAllLoading(false)
    }
  }, [refresh, t])

  const detectChannelUpdates = useCallback(
    async (ch: UpstreamUpdateChannel | null) => {
      if (applyRef.current || !ch?.id) return
      const requestId = ++previewRequestRef.current
      setChannel(ch)
      setShowModal(true)
      setDetectLoading(true)
      setDetectError(null)
      setApplyError(null)
      setResult(null)
      setAddModels([])
      setRemoveModels([])
      try {
        const res = await api.post<
          UpstreamUpdateResponse<{
            add_models: string[] | null
            remove_models: string[] | null
          }>
        >(
          '/api/channel/upstream_updates/detect',
          { id: ch.id },
          upstreamUpdateRequestConfig
        )
        if (requestId !== previewRequestRef.current) return
        if (!res.data?.success || !res.data.data) {
          throw createServerError(res.data, t('Detection failed'))
        }
        const added = normalizeModelList(res.data.data.add_models ?? [])
        setAddModels(added)
        setRemoveModels(normalizeModelList(res.data.data.remove_models ?? []))
        setPreferredTab(added.length ? 'add' : 'remove')
        setPreviewVersion((version) => version + 1)
      } catch (error: unknown) {
        if (requestId !== previewRequestRef.current) return
        setDetectError(getServerErrorMessage(error, t('Detection failed')))
        handleServerError(error, t('Detection failed'))
      } finally {
        if (requestId === previewRequestRef.current) setDetectLoading(false)
      }
    },
    [t]
  )

  const detectAllUpdates = useCallback(async () => {
    if (detectAllRef.current) return
    detectAllRef.current = true
    setDetectAllLoading(true)
    try {
      const res = await api.post(
        '/api/channel/upstream_updates/detect_all',
        {},
        upstreamUpdateRequestConfig
      )
      const { success } = res.data || {}
      if (!success) {
        handleServerError(res.data, t('Batch detection failed'))
        return
      }

      toast.success(
        t(
          'Upstream model detection task started. Track progress in System Info, then refresh to review staged updates.'
        )
      )
      await refresh()
    } catch (e: unknown) {
      handleServerError(e, t('Batch detection failed'))
    } finally {
      detectAllRef.current = false
      setDetectAllLoading(false)
    }
  }, [refresh, t])

  // Memoized so consumers (and the channels context value built from this) get
  // a stable reference unless an actual field changes. Callbacks above are all
  // useCallback-stable, so this only changes when relevant state changes.
  return useMemo(
    () => ({
      showModal,
      channel,
      addModels,
      removeModels,
      preferredTab,
      applyLoading,
      detectLoading,
      detectError,
      applyError,
      result,
      previewVersion,
      detectAllLoading,
      applyAllLoading,
      openModal,
      closeModal,
      applyUpdates,
      applyAllUpdates,
      detectChannelUpdates,
      detectAllUpdates,
    }),
    [
      showModal,
      channel,
      addModels,
      removeModels,
      preferredTab,
      applyLoading,
      detectLoading,
      detectError,
      applyError,
      result,
      previewVersion,
      detectAllLoading,
      applyAllLoading,
      openModal,
      closeModal,
      applyUpdates,
      applyAllUpdates,
      detectChannelUpdates,
      detectAllUpdates,
    ]
  )
}

export type ChannelUpstreamUpdateState = ReturnType<
  typeof useChannelUpstreamUpdates
>
