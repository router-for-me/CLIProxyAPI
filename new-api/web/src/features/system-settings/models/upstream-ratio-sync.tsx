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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckSquare, RefreshCcw } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import {
  buildPricingChanges,
  getModelPricing,
  saveModelPricing,
  invalidateModelPricing,
  type ModelPricingConfig,
} from '@/features/model-pricing/api'
import { applyPriceSyncSelections } from '@/features/model-pricing/pricing'
import { handleServerError } from '@/lib/handle-server-error'
import {
  requireServerSuccess,
  createServerError,
} from '@/lib/server-error-message'

import { fetchUpstreamRatios, getUpstreamChannels } from '../api'
import type {
  DifferencesMap,
  PricingSyncModels,
  PricingSyncValues,
  UpstreamChannel,
  UpstreamConfig,
} from '../types'
import { ChannelSelectorDialog } from './channel-selector-dialog'
import {
  ConflictConfirmDialog,
  type ConflictItem,
} from './conflict-confirm-dialog'
import {
  DEFAULT_ENDPOINT,
  MODELS_DEV_PRESET_ENDPOINT,
  MODELS_DEV_PRESET_ID,
  OFFICIAL_CHANNEL_ENDPOINT,
  OFFICIAL_CHANNEL_ID,
  OPENROUTER_CHANNEL_TYPE,
  OPENROUTER_ENDPOINT,
} from './constants'
import {
  describeSyncPrice,
  getUpstreamDisplayName,
  type PricingSourceSelection,
  type PricingSourceSelections,
} from './upstream-ratio-sync-helpers'
import { UpstreamRatioSyncTable } from './upstream-ratio-sync-table'

function getDefaultEndpointForChannel(channel: UpstreamChannel): string {
  if (channel.id === MODELS_DEV_PRESET_ID) return MODELS_DEV_PRESET_ENDPOINT
  if (channel.id === OFFICIAL_CHANNEL_ID) return OFFICIAL_CHANNEL_ENDPOINT
  if (channel.type === OPENROUTER_CHANNEL_TYPE) return OPENROUTER_ENDPOINT
  return DEFAULT_ENDPOINT
}

export function UpstreamRatioSync() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [pricingBaseline, setPricingBaseline] =
    useState<ModelPricingConfig | null>(null)
  const [channelDialogOpen, setChannelDialogOpen] = useState(false)
  const [conflictDialogOpen, setConflictDialogOpen] = useState(false)
  const [selectedChannelIds, setSelectedChannelIds] = useState<number[]>([])
  const [channelEndpoints, setChannelEndpoints] = useState<
    Record<number, string>
  >({})
  const [differences, setDifferences] = useState<DifferencesMap>({})
  const [prices, setPrices] = useState<PricingSyncModels>({})
  const [selectedSources, setSelectedSources] =
    useState<PricingSourceSelections>({})
  const [conflictItems, setConflictItems] = useState<ConflictItem[]>([])
  const [loadingBaseline, setLoadingBaseline] = useState(false)
  const { data: channelsData } = useQuery({
    queryKey: ['upstream-channels'],
    queryFn: async () => requireServerSuccess(await getUpstreamChannels()),
    enabled: channelDialogOpen,
  })
  const channels = useMemo(() => channelsData?.data ?? [], [channelsData?.data])
  useEffect(() => {
    if (!channels.length) return
    setChannelEndpoints((previous) => {
      const next = { ...previous }
      for (const channel of channels) {
        next[channel.id] ??= getDefaultEndpointForChannel(channel)
      }
      return next
    })
  }, [channels])
  const resolutions = useMemo(
    () =>
      Object.fromEntries(
        Object.entries(selectedSources).flatMap(([name, source]) => {
          const values = prices[name]?.upstreams[source]
          return values ? [[name, { ...values }]] : []
        })
      ) as Record<string, Record<string, number | string>>,
    [selectedSources, prices]
  )
  const fetchMutation = useMutation({
    mutationFn: async (request: Parameters<typeof fetchUpstreamRatios>[0]) => {
      const response = await fetchUpstreamRatios(request)
      if (!response.success || !response.data?.prices) {
        throw createServerError(response, t('Failed to fetch upstream prices'))
      }
      const results = response.data.test_results
      if (
        results.length &&
        results.every((result) => result.status === 'error')
      ) {
        throw new Error(
          results
            .map(
              (result) =>
                `${getUpstreamDisplayName(result.name, t)}: ${result.error}`
            )
            .join(', ')
        )
      }
      return response
    },
    onMutate: () => {
      setPrices({})
      setDifferences({})
      setSelectedSources({})
    },
    onSuccess: (response) => {
      const errors = response.data.test_results.filter(
        (result) => result.status === 'error'
      )
      if (errors.length) {
        toast.warning(
          t('Some channels failed: {{errorMsg}}', {
            errorMsg: errors
              .map(
                (result) =>
                  `${getUpstreamDisplayName(result.name, t)}: ${result.error}`
              )
              .join(', '),
          })
        )
      }
      setDifferences(response.data.differences)
      setPrices(response.data.prices)
      if (!Object.keys(response.data.prices).length) {
        toast.success(t('No price differences found'))
      }
    },
    onError: (error: Error) =>
      handleServerError(error, t('Failed to fetch upstream prices')),
  })
  const syncMutation = useMutation({
    mutationFn: async () => {
      if (!pricingBaseline) throw new Error(t('Reload pricing'))
      const after = applyPriceSyncSelections(
        pricingBaseline.options,
        resolutions
      )
      await saveModelPricing(
        buildPricingChanges(pricingBaseline, pricingBaseline.options, after)
      )
      return resolutions
    },
    onSuccess: async (saved) => {
      setPrices((previous) =>
        Object.fromEntries(
          Object.entries(previous).map(([name, row]) => [
            name,
            { ...row, current: saved[name] ?? row.current },
          ])
        )
      )
      setSelectedSources({})
      setConflictDialogOpen(false)
      setPricingBaseline(null)
      toast.success(t('Prices synced successfully'))
      await invalidateModelPricing(queryClient)
      try {
        setPricingBaseline(await getModelPricing())
      } catch (error) {
        handleServerError(error, t('Reload pricing'))
      }
    },
    onError: (error: Error) =>
      handleServerError(error, t('Failed to sync prices')),
  })
  const handleConfirmChannelSelection = async (selectedIds: number[]) => {
    const selected = channels.filter((channel) =>
      selectedIds.includes(channel.id)
    )
    if (!selected.length) {
      toast.warning(t('Please select at least one channel'))
      return
    }
    const upstreams: UpstreamConfig[] = selected.map((channel) => ({
      id: channel.id,
      name: channel.name,
      base_url: channel.base_url,
      endpoint:
        channelEndpoints[channel.id] || getDefaultEndpointForChannel(channel),
    }))
    setLoadingBaseline(true)
    setPrices({})
    setDifferences({})
    fetchMutation.reset()
    setPricingBaseline(null)
    setSelectedSources({})
    syncMutation.reset()
    try {
      setPricingBaseline(await getModelPricing())
      fetchMutation.mutate({ upstreams, timeout: 10 })
    } catch (error) {
      handleServerError(error, t('Failed to load model pricing'))
    } finally {
      setLoadingBaseline(false)
    }
  }
  const handleSelectPrices = useCallback(
    (selections: PricingSourceSelection[]) => {
      setSelectedSources((previous) =>
        Object.fromEntries([
          ...Object.entries(previous),
          ...selections
            .filter(
              (selection) =>
                prices[selection.model]?.upstreams[selection.source]
            )
            .map((selection) => [selection.model, selection.source]),
        ])
      )
    },
    [prices]
  )
  const handleUnselectPrices = useCallback((models: string[]) => {
    setSelectedSources((previous) => {
      const next = { ...previous }
      for (const name of models) delete next[name]
      return next
    })
  }, [])
  const handleApplySync = () => {
    const previews: ConflictItem[] = Object.entries(selectedSources).flatMap(
      ([model, source]) => {
        const row = prices[model]
        const selected: PricingSyncValues | undefined = row?.upstreams[source]
        if (!row || !selected) return []
        return [
          {
            model,
            channel: getUpstreamDisplayName(source, t),
            current: describeSyncPrice(row.current, t),
            newVal: describeSyncPrice(selected, t),
          },
        ]
      }
    )
    setConflictItems(previews)
    setConflictDialogOpen(true)
  }
  const isLoading =
    loadingBaseline || fetchMutation.isPending || syncMutation.isPending
  return (
    <div className='flex h-full min-h-0 flex-col gap-3'>
      <div className='min-h-0 flex-1'>
        {fetchMutation.isError ? (
          <ErrorState
            description={fetchMutation.error.message}
            action={
              <Button
                variant='outline'
                onClick={() =>
                  void handleConfirmChannelSelection(selectedChannelIds)
                }
              >
                {t('Retry')}
              </Button>
            }
          />
        ) : (
          <UpstreamRatioSyncTable
            toolbar={
              <Button
                variant='outline'
                onClick={() => setChannelDialogOpen(true)}
                disabled={isLoading}
              >
                <RefreshCcw className='size-4' aria-hidden />
                {t('Select price sources')}
              </Button>
            }
            prices={prices}
            differences={differences}
            selectedSources={selectedSources}
            isDisabled={isLoading}
            isSyncing={fetchMutation.isPending || loadingBaseline}
            onSelectPrices={handleSelectPrices}
            onUnselectPrices={handleUnselectPrices}
          />
        )}
      </div>
      <div className='flex shrink-0 flex-wrap items-center justify-between gap-2 border-t pt-3'>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='text-muted-foreground text-sm' role='status'>
            {t('{{count}} selected models', {
              count: Object.keys(selectedSources).length,
            })}
          </span>
          {Object.keys(selectedSources).length > 0 && (
            <Button
              variant='ghost'
              size='sm'
              disabled={isLoading}
              onClick={() => setSelectedSources({})}
            >
              {t('Clear selection')}
            </Button>
          )}
        </div>
        <Button
          onClick={handleApplySync}
          disabled={
            !pricingBaseline ||
            !Object.keys(selectedSources).length ||
            isLoading
          }
        >
          <CheckSquare className='size-4' aria-hidden />
          {t('Apply Sync')}
        </Button>
      </div>
      <ChannelSelectorDialog
        open={channelDialogOpen}
        onOpenChange={setChannelDialogOpen}
        channels={channels}
        selectedChannelIds={selectedChannelIds}
        onSelectedChannelIdsChange={setSelectedChannelIds}
        channelEndpoints={channelEndpoints}
        onChannelEndpointsChange={setChannelEndpoints}
        onConfirm={handleConfirmChannelSelection}
      />
      <ConflictConfirmDialog
        open={conflictDialogOpen}
        onOpenChange={(open) => {
          if (!syncMutation.isPending) setConflictDialogOpen(open)
        }}
        conflicts={conflictItems}
        onConfirm={() => syncMutation.mutate()}
        isLoading={syncMutation.isPending}
        error={syncMutation.error?.message}
        onReload={() => {
          setConflictDialogOpen(false)
          setSelectedSources({})
          setPrices({})
          setDifferences({})
          setPricingBaseline(null)
          syncMutation.reset()
          setChannelDialogOpen(true)
        }}
      />
    </div>
  )
}
