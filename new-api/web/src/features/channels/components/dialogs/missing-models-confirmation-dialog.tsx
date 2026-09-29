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
import { useTranslation } from 'react-i18next'

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'

export type MissingModelsAction = 'cancel' | 'submit' | 'add'

type MissingModelsConfirmationDialogProps = {
  open: boolean
  missingModels: string[]
  onConfirm: (action: MissingModelsAction) => void
  onOpenChange?: (open: boolean) => void
}

/**
 * Confirmation dialog shown when models in model_mapping are missing from the models list
 * Provides three options:
 * 1. Cancel - Go back to edit
 * 2. Submit - Submit anyway without adding missing models
 * 3. Add - Automatically add missing models and submit
 */
export function MissingModelsConfirmationDialog({
  open,
  missingModels,
  onConfirm,
  onOpenChange,
}: MissingModelsConfirmationDialogProps) {
  const { t } = useTranslation()

  const handleOpenChange = (newOpen: boolean) => {
    if (!newOpen) {
      onConfirm('cancel')
    }
    onOpenChange?.(newOpen)
  }

  return (
    <AlertDialog open={open} onOpenChange={handleOpenChange}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            {t('Models not in list, may fail to invoke')}
          </AlertDialogTitle>
          <AlertDialogDescription
            render={<div className='space-y-3 text-sm' />}
          >
            <div>
              {t(
                'The following models in the model redirect have not been added to the "Models" list and may fail during invocation due to missing available models:'
              )}
            </div>
            <div className='rounded-md bg-red-50 p-2 font-mono text-xs break-all text-red-600 dark:bg-red-950/50 dark:text-red-400'>
              {missingModels.join(', ')}
            </div>
            <div>
              {t(
                'You can manually add them in "Custom Model Names", click "Fill" and then submit, or use the operations below to handle automatically.'
              )}
            </div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter className='flex-col gap-2 sm:flex-row'>
          <AlertDialogCancel onClick={() => onConfirm('cancel')}>
            {t('Go back and edit')}
          </AlertDialogCancel>
          <Button variant='secondary' onClick={() => onConfirm('submit')}>
            {t('Submit directly')}
          </Button>
          <AlertDialogAction onClick={() => onConfirm('add')}>
            {t('Add and submit')}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
