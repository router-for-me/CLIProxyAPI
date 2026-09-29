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
import { Copy, Check } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { ScrollArea } from '@/components/ui/scroll-area'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'

interface PromptDialogProps {
  prompt: string
  promptEn?: string
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function PromptDialog({
  prompt,
  promptEn,
  open,
  onOpenChange,
}: PromptDialogProps) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Prompt Details')}
      description={t('View the complete prompt and its English translation')}
      contentClassName='sm:max-w-lg'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <ScrollArea className='max-h-[500px] pr-4'>
        <div className='space-y-4 py-4'>
          {/* Original Prompt */}
          <div className='space-y-2'>
            <Label className='text-sm font-semibold'>{t('Prompt')}</Label>
            <div className='bg-muted/50 relative rounded-md border p-3'>
              <Button
                variant='ghost'
                size='sm'
                className='absolute top-2 right-2 h-8 w-8 p-0'
                onClick={() => copyToClipboard(prompt)}
                title={t('Copy to clipboard')}
              >
                {copiedText === prompt ? (
                  <Check className='size-4 text-green-600' />
                ) : (
                  <Copy className='size-4' />
                )}
              </Button>
              <p className='pr-10 text-sm leading-relaxed break-words whitespace-pre-wrap'>
                {prompt || '-'}
              </p>
            </div>
          </div>

          {/* English Prompt */}
          {promptEn && (
            <div className='space-y-2'>
              <Label className='text-sm font-semibold'>
                {t('Prompt (EN)')}
              </Label>
              <div className='bg-muted/50 relative rounded-md border p-3'>
                <Button
                  variant='ghost'
                  size='sm'
                  className='absolute top-2 right-2 h-8 w-8 p-0'
                  onClick={() => copyToClipboard(promptEn)}
                  title={t('Copy to clipboard')}
                >
                  {copiedText === promptEn ? (
                    <Check className='size-4 text-green-600' />
                  ) : (
                    <Copy className='size-4' />
                  )}
                </Button>
                <p className='pr-10 text-sm leading-relaxed break-words whitespace-pre-wrap'>
                  {promptEn}
                </p>
              </div>
            </div>
          )}
        </div>
      </ScrollArea>
    </Dialog>
  )
}
