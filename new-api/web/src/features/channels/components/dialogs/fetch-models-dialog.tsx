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
import { useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useState, useEffect, useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { handleServerError } from '@/lib/handle-server-error'

import { fetchUpstreamModels, updateChannel } from '../../api'
import {
  channelsQueryKeys,
  normalizeModelName,
  parseModelsString,
} from '../../lib'
import { useChannels } from '../channels-provider'
import { UpstreamModelSelection } from '../upstream-model-selection'

function normalizeModelNameList(models: readonly string[]): string[] {
  return [...new Set(models.map((m) => normalizeModelName(m)).filter(Boolean))]
}

type FetchModelsDialogBaseProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  redirectModels?: string[]
  redirectSourceModels?: string[]
  customFetcher?: () => Promise<string[]>
  channelName?: string | null
}

type FetchModelsDialogProps = FetchModelsDialogBaseProps &
  (
    | {
        onModelsSelected: (models: string[]) => void
        existingModelsOverride: string[]
      }
    | {
        onModelsSelected?: undefined
        existingModelsOverride?: undefined
      }
  )

export function FetchModelsDialog({
  open,
  onOpenChange,
  onModelsSelected,
  redirectModels = [],
  redirectSourceModels = [],
  customFetcher,
  existingModelsOverride,
  channelName,
}: FetchModelsDialogProps) {
  const { t } = useTranslation()
  const { currentRow } = useChannels()
  const activeChannel = customFetcher ? null : currentRow
  const queryClient = useQueryClient()
  const [isFetching, setIsFetching] = useState(false)
  const [isSaving, setIsSaving] = useState(false)
  const [fetchedModels, setFetchedModels] = useState<string[]>([])
  const [candidateModels, setCandidateModels] = useState<string[]>([])
  const [selectedModels, setSelectedModels] = useState<string[]>([])

  // Parse existing models
  const existingModels = useMemo(
    () =>
      existingModelsOverride ?? parseModelsString(activeChannel?.models || ''),
    [existingModelsOverride, activeChannel?.models]
  )

  const fetchedModelSet = new Set(normalizeModelNameList(fetchedModels))
  const redirectSourceSet = new Set(
    normalizeModelNameList(redirectSourceModels)
  )
  const hasUnlistedModels = normalizeModelNameList([
    ...candidateModels,
    ...selectedModels,
  ]).some(
    (model) => !fetchedModelSet.has(model) && !redirectSourceSet.has(model)
  )

  useEffect(() => {
    if (open && (activeChannel || customFetcher)) {
      handleFetchModels()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, activeChannel?.id, customFetcher])

  const handleFetchModels = async () => {
    if (!activeChannel && !customFetcher) return

    setIsFetching(true)
    try {
      if (customFetcher) {
        const list = await customFetcher()
        setFetchedModels(list)
        setCandidateModels(existingModels)
        setSelectedModels(existingModels)
        toast.success(t('Fetched {{count}} models', { count: list.length }))
      } else if (activeChannel) {
        const response = await fetchUpstreamModels(activeChannel.id)
        if (response.success) {
          const list = Array.isArray(response.data) ? response.data : []
          setFetchedModels(list)
          setCandidateModels(existingModels)
          setSelectedModels(existingModels)
          toast.success(t('Fetched {{count}} models', { count: list.length }))
        } else {
          handleServerError(response, t('Failed to fetch models'))
          setFetchedModels([])
        }
      }
    } catch (error: unknown) {
      handleServerError(error, t('Failed to fetch models'))
      setFetchedModels([])
    } finally {
      setIsFetching(false)
    }
  }

  const handleSave = async () => {
    // If onModelsSelected callback is provided, use it (form filling mode)
    if (onModelsSelected) {
      onModelsSelected(selectedModels)
      toast.success(t('Models filled to form'))
      onOpenChange(false)
      return
    }

    // Otherwise, directly save to API (standalone mode)
    if (!activeChannel) return
    setIsSaving(true)
    try {
      const modelsString = selectedModels.join(',')
      const response = await updateChannel(activeChannel.id, {
        models: modelsString,
      })
      if (response.success) {
        toast.success(t('Models updated successfully'))
        queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })
        onOpenChange(false)
      } else {
        handleServerError(response, t('Failed to update models'))
      }
    } catch (error: unknown) {
      handleServerError(error, t('Failed to update models'))
    } finally {
      setIsSaving(false)
    }
  }

  const handleClose = () => {
    setFetchedModels([])
    setCandidateModels([])
    setSelectedModels([])
    onOpenChange(false)
  }

  const showFooterActions =
    !!(activeChannel || customFetcher) &&
    !isFetching &&
    (fetchedModels.length > 0 || hasUnlistedModels)

  let dialogDescription: ReactNode = t('Fetch available models from upstream')
  if (activeChannel) {
    dialogDescription = (
      <>
        {t('Channel:')} <strong>{activeChannel.name}</strong>
      </>
    )
  } else if (channelName) {
    dialogDescription = (
      <>
        {t('Channel:')} <strong>{channelName}</strong>
      </>
    )
  }

  let dialogBody: ReactNode
  if (!activeChannel && !customFetcher) {
    dialogBody = (
      <div className='text-muted-foreground py-8 text-center'>
        {t('No channel selected')}
      </div>
    )
  } else if (isFetching) {
    dialogBody = (
      <div className='flex items-center justify-center py-12'>
        <Loader2 className='text-muted-foreground h-8 w-8 animate-spin' />
      </div>
    )
  } else if (fetchedModels.length === 0 && !hasUnlistedModels) {
    dialogBody = (
      <div className='text-muted-foreground py-8 text-center'>
        <p>{t('No models fetched yet.')}</p>
        <Button
          className='mt-4'
          onClick={handleFetchModels}
          disabled={isFetching}
        >
          {t('Fetch Models')}
        </Button>
      </div>
    )
  } else {
    dialogBody = (
      <UpstreamModelSelection
        models={fetchedModels}
        selected={selectedModels}
        onChange={setSelectedModels}
        existingModels={existingModels}
        redirectModels={redirectModels}
        redirectSourceModels={redirectSourceModels}
      />
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={handleClose}
      title={t('Fetch Models')}
      description={dialogDescription}
      contentClassName='max-w-3xl'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        showFooterActions ? (
          <>
            <Button variant='outline' onClick={handleClose} disabled={isSaving}>
              {t('Cancel')}
            </Button>
            <Button onClick={handleSave} disabled={isSaving}>
              {isSaving && <Loader2 className='mr-2 h-4 w-4 animate-spin' />}
              {isSaving ? t('Saving...') : t('Save Models')}
            </Button>
          </>
        ) : null
      }
    >
      {dialogBody}
    </Dialog>
  )
}
