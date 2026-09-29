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

import { Dialog } from '@/components/dialog'
import { ScrollArea } from '@/components/ui/scroll-area'

type DescriptionDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  modelName: string
  description: string
}

export function DescriptionDialog({
  open,
  onOpenChange,
  modelName,
  description,
}: DescriptionDialogProps) {
  const { t } = useTranslation()
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={modelName}
      description={t('Model Description')}
      contentClassName='max-w-2xl'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <ScrollArea className='max-h-96'>
        <div className='space-y-2 pr-4'>
          <p className='text-foreground text-sm leading-relaxed break-words whitespace-pre-wrap'>
            {description}
          </p>
        </div>
      </ScrollArea>
    </Dialog>
  )
}
