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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2, AlertCircle } from 'lucide-react'
import { useState, useEffect, useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { MultiSelect } from '@/components/multi-select'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import {
  getTagModels,
  editTagChannels,
  getAllModels,
  getGroups,
} from '../../api'
import { channelsQueryKeys } from '../../lib'
import type { TagOperationParams } from '../../types'
import { useChannels } from '../channels-provider'
import { ModelMappingEditor } from '../model-mapping-editor'

type TagBatchEditDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function TagBatchEditDialog({
  open,
  onOpenChange,
}: TagBatchEditDialogProps) {
  const { t } = useTranslation()
  const { currentTag } = useChannels()
  const queryClient = useQueryClient()
  const [isLoading, setIsLoading] = useState(false)
  const [isSaving, setIsSaving] = useState(false)

  // Form fields
  const [newTag, setNewTag] = useState('')
  const [models, setModels] = useState('')
  const [modelMapping, setModelMapping] = useState('')
  const [groups, setGroups] = useState<string[]>([])

  // Fetch available groups
  const { data: groupsData, isLoading: isLoadingGroups } = useQuery({
    queryKey: ['groups'],
    queryFn: async () => requireServerSuccess(await getGroups()),
  })

  // Transform groups to multi-select options
  const groupOptions = useMemo(() => {
    if (!groupsData?.data) return []
    const allGroups = new Set([...groupsData.data, ...groups])
    return [...allGroups].map((group) => ({
      value: group,
      label: group,
    }))
  }, [groupsData, groups])

  useEffect(() => {
    if (open && currentTag) {
      loadTagData()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, currentTag])

  const loadTagData = async () => {
    if (!currentTag) return

    setIsLoading(true)
    try {
      // Fetch current tag models
      const tagModelsResponse = await getTagModels(currentTag)
      requireServerSuccess(tagModelsResponse)
      if (tagModelsResponse.success && tagModelsResponse.data) {
        setModels(tagModelsResponse.data)
      }

      // Fetch all available models (for future use if needed)
      const allModelsResponse = requireServerSuccess(await getAllModels())
      if (allModelsResponse.success && allModelsResponse.data) {
        // Available models could be used for autocomplete in the future
      }

      // Initialize new tag with current tag name
      setNewTag(currentTag)
    } catch (_error: unknown) {
      handleServerError(_error, t('Failed to load tag data'))
    } finally {
      setIsLoading(false)
    }
  }

  const handleSave = async () => {
    if (!currentTag) return

    // Validate model mapping JSON if provided
    if (modelMapping.trim()) {
      try {
        JSON.parse(modelMapping)
      } catch {
        toast.error(t('Model mapping must be valid JSON'))
        return
      }
    }

    setIsSaving(true)
    try {
      const params: Record<string, string | undefined> = {
        tag: currentTag,
      }

      if (newTag !== currentTag) {
        params.new_tag = newTag || undefined
      }

      if (models.trim()) {
        params.models = models
      }

      if (modelMapping.trim()) {
        params.model_mapping = modelMapping
      }

      if (groups.length > 0) {
        params.groups = groups.join(',')
      }

      // Check if there are any changes
      if (Object.keys(params).length === 1) {
        toast.warning(t('No changes made'))
        return
      }

      const response = await editTagChannels(
        params as unknown as TagOperationParams
      )
      if (response.success) {
        toast.success(t('Tag updated successfully'))
        queryClient.invalidateQueries({ queryKey: channelsQueryKeys.lists() })
        handleClose()
      } else {
        handleServerError(response, t('Failed to update tag'))
      }
    } catch (error: unknown) {
      handleServerError(error, t('Failed to update tag'))
    } finally {
      setIsSaving(false)
    }
  }

  const handleClose = () => {
    setNewTag('')
    setModels('')
    setModelMapping('')
    setGroups([])
    onOpenChange(false)
  }

  if (!currentTag) return null

  return (
    <Dialog
      open={open}
      onOpenChange={handleClose}
      title={t('Batch Edit by Tag')}
      description={
        <>
          {t('Edit all channels with tag:')}
          <strong>{currentTag}</strong>
        </>
      }
      contentClassName='max-w-2xl'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        !isLoading ? (
          <>
            <Button variant='outline' onClick={handleClose} disabled={isSaving}>
              {t('Cancel')}
            </Button>
            <Button onClick={handleSave} disabled={isSaving}>
              {isSaving ? (
                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
              ) : null}
              {isSaving ? t('Saving...') : t('Save Changes')}
            </Button>
          </>
        ) : null
      }
    >
      {isLoading ? (
        <div className='flex items-center justify-center py-12'>
          <Loader2 className='text-muted-foreground h-8 w-8 animate-spin' />
        </div>
      ) : (
        <div className='space-y-4 py-4'>
          <Alert>
            <AlertCircle className='h-4 w-4' />
            <AlertDescription>
              {t(
                'All edits are overwrite operations. Leave fields empty to keep current values unchanged.'
              )}
            </AlertDescription>
          </Alert>

          {/* Tag Name */}
          <div className='space-y-2'>
            <Label htmlFor='new-tag'>{t('Tag Name')}</Label>
            <Input
              id='new-tag'
              placeholder={t('Enter new tag name (leave empty to disband tag)')}
              value={newTag}
              onChange={(e) => setNewTag(e.target.value)}
              disabled={isSaving}
            />
            <p className='text-muted-foreground text-xs'>
              {t('Leave empty to disband the tag')}
            </p>
          </div>

          {/* Models */}
          <div className='space-y-2'>
            <Label htmlFor='models'>{t('Models')}</Label>
            <Textarea
              id='models'
              placeholder={t(
                'Comma-separated model names (leave empty to keep current)'
              )}
              value={models}
              onChange={(e) => setModels(e.target.value)}
              disabled={isSaving}
              rows={3}
            />
            <p className='text-muted-foreground text-xs'>
              {t(
                'Current models for the longest channel in this tag. May not include all models from all channels.'
              )}
            </p>
          </div>

          {/* Model Mapping */}
          <div className='space-y-2'>
            <Label htmlFor='model-mapping'>{t('Model Mapping')}</Label>
            <ModelMappingEditor
              value={modelMapping}
              onChange={setModelMapping}
              disabled={isSaving}
            />
          </div>

          {/* Groups */}
          <div className='space-y-2'>
            <Label htmlFor='groups'>{t('Groups')}</Label>
            {isLoadingGroups ? (
              <Skeleton className='h-10 w-full' />
            ) : (
              <MultiSelect
                options={groupOptions}
                selected={groups}
                onChange={setGroups}
                placeholder={t('Select groups (leave empty to keep current)')}
              />
            )}
            <p className='text-muted-foreground text-xs'>
              {t('User groups that can access channels with this tag')}
            </p>
          </div>
        </div>
      )}
    </Dialog>
  )
}
