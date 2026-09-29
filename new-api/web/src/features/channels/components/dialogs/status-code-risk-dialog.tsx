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
import { AlertTriangle } from 'lucide-react'
import { lazy, Suspense, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'

const Markdown = lazy(() =>
  import('@/components/ui/markdown').then((module) => ({
    default: module.Markdown,
  }))
)

interface StatusCodeRiskDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  detailItems: string[]
  onConfirm: () => void
}

const CHECKLIST_KEYS = [
  'High-risk status code retry risk check 1',
  'High-risk status code retry risk check 2',
  'High-risk status code retry risk check 3',
  'High-risk status code retry risk check 4',
] as const

export function StatusCodeRiskDialog({
  open,
  onOpenChange,
  detailItems,
  onConfirm,
}: StatusCodeRiskDialogProps) {
  const { t } = useTranslation()
  const [checkedItems, setCheckedItems] = useState<Set<number>>(new Set())
  const [confirmText, setConfirmText] = useState('')

  const requiredText = t('High-risk status code retry confirmation text')
  const allChecked = checkedItems.size === CHECKLIST_KEYS.length
  const textMatches = confirmText.trim() === requiredText.trim()
  const canConfirm = allChecked && textMatches

  const handleConfirm = () => {
    if (!canConfirm) return
    setCheckedItems(new Set())
    setConfirmText('')
    onConfirm()
  }

  const handleCancel = () => {
    setCheckedItems(new Set())
    setConfirmText('')
    onOpenChange(false)
  }

  const toggleCheck = (idx: number) => {
    setCheckedItems((prev) => {
      const next = new Set(prev)
      if (next.has(idx)) next.delete(idx)
      else next.add(idx)
      return next
    })
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        <>
          <AlertTriangle className='h-5 w-5' />
          {t('High-risk operation confirmation')}
        </>
      }
      contentClassName='sm:max-w-3xl'
      titleClassName='text-destructive flex items-center gap-2'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button variant='outline' onClick={handleCancel}>
            {t('Cancel')}
          </Button>
          <Button
            variant='destructive'
            disabled={!canConfirm}
            onClick={handleConfirm}
          >
            {t('I confirm enabling high-risk retry')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        {open ? (
          <div className='border-warning/40 bg-warning/5 rounded-lg border p-3 sm:p-4'>
            <Suspense
              fallback={
                <div
                  aria-hidden='true'
                  className='bg-warning/10 h-32 animate-pulse rounded-md'
                />
              }
            >
              <Markdown className='[&_h3]:text-warning text-sm [&_h3]:text-base'>
                {t('High-risk status code retry risk disclaimer')}
              </Markdown>
            </Suspense>
          </div>
        ) : null}

        {detailItems.length > 0 && (
          <div className='border-destructive/30 bg-destructive/5 rounded-lg border p-3'>
            <p className='mb-2 text-sm font-medium'>
              {t('Detected high-risk status code redirect rules')}
            </p>
            <ul className='list-inside list-disc text-sm'>
              {detailItems.map((item) => (
                <li key={item} className='font-mono text-xs'>
                  {item}
                </li>
              ))}
            </ul>
          </div>
        )}

        <div className='space-y-2'>
          {CHECKLIST_KEYS.map((key, idx) => (
            <div key={key} className='flex items-start gap-2'>
              <Checkbox
                id={`risk-check-${idx}`}
                checked={checkedItems.has(idx)}
                onCheckedChange={() => toggleCheck(idx)}
              />
              <Label
                htmlFor={`risk-check-${idx}`}
                className='text-sm leading-tight'
              >
                {t(key)}
              </Label>
            </div>
          ))}
        </div>

        <div className='space-y-1.5'>
          <Label className='text-sm'>
            {t('Action confirmation')}:{' '}
            <code className='bg-muted rounded px-1 text-xs'>
              {requiredText}
            </code>
          </Label>
          <Input
            value={confirmText}
            onChange={(e) => setConfirmText(e.target.value)}
            placeholder={t('High-risk status code retry input placeholder')}
          />
          {confirmText && !textMatches && (
            <p className='text-destructive text-xs'>
              {t('High-risk status code retry input mismatch')}
            </p>
          )}
        </div>
      </div>
    </Dialog>
  )
}
