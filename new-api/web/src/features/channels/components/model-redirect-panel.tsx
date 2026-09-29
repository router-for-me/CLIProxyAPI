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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import {
  FloatingWindow,
  type FloatingWindowPosition,
} from '@/components/floating-window'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'

import {
  ModelMappingEditor,
  type ModelMappingDraftRequest,
} from './model-mapping-editor'

type ModelRedirectPanelProps = {
  defaultPosition: FloatingWindowPosition
  width: number
  onClose: () => void
  mappingCount: number
  mappingValue: string
  onMappingChange: (value: string) => void
  /** Called once an edit settles; the drawer syncs the model list here. */
  onMappingCommit: (value: string) => void
  onBatchAdd: () => void
  syncModels: boolean
  onSyncModelsChange: (value: boolean) => void
  sourceModelOptions: string[]
  targetModelOptions: string[]
  draftRequest: ModelMappingDraftRequest | null
  onDraftRequestHandled: () => void
  disabled?: boolean
}

/**
 * Floating workbench for model redirects. It is mounted inside the channel
 * drawer so the modal sheet treats it as its own content, and floats above
 * the page from there.
 */
export function ModelRedirectPanel(props: ModelRedirectPanelProps) {
  const { t } = useTranslation()
  const id = useId()

  return (
    <FloatingWindow
      title={t('Model redirects')}
      badge={
        <Badge variant='secondary' className='h-4 px-1 text-[10px]'>
          {props.mappingCount}
        </Badge>
      }
      defaultPosition={props.defaultPosition}
      defaultWidth={props.width}
      minWidth={340}
      storageKey='channel-model-redirects'
      expandRequest={props.draftRequest?.token}
      onClose={props.onClose}
      footer={
        <div className='flex items-start gap-3'>
          <Checkbox
            id={`${id}-sync`}
            className='mt-0.5'
            checked={props.syncModels}
            onCheckedChange={(checked) =>
              props.onSyncModelsChange(checked === true)
            }
          />
          <Label
            htmlFor={`${id}-sync`}
            className='text-xs leading-5 font-normal'
          >
            {t(
              'Publish the request names and remove the raw upstream names from the model list'
            )}
          </Label>
        </div>
      }
    >
      <ModelMappingEditor
        value={props.mappingValue}
        onChange={props.onMappingChange}
        onCommit={props.onMappingCommit}
        onBatchAdd={props.onBatchAdd}
        disabled={props.disabled}
        sourceModelOptions={props.sourceModelOptions}
        targetModelOptions={props.targetModelOptions}
        draftRequest={props.draftRequest}
        onDraftRequestHandled={props.onDraftRequestHandled}
      />
    </FloatingWindow>
  )
}
