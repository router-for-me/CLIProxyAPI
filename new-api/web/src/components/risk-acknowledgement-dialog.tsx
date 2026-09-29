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
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { cn } from '@/lib/utils'

type RequiredTextPart = {
  type: 'input' | 'static'
  text: string
  placeholder?: string
}

type NormalizedRequiredTextPart = RequiredTextPart & {
  inputIndex?: number
}

type RiskAcknowledgementDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description?: React.ReactNode
  items?: string[]
  checklist?: string[]
  requiredText?: string
  requiredTextParts?: RequiredTextPart[]
  inputPrompt?: string
  inputPlaceholder?: string
  mismatchHint?: string
  confirmText?: string
  cancelText?: string
  destructive?: boolean
  isLoading?: boolean
  onConfirm: () => void
  className?: string
}

function getRequiredTextRows(text: string) {
  return Math.max(1, Math.ceil(Array.from(text).length / 42))
}

export function RiskAcknowledgementDialog({
  open,
  onOpenChange,
  title,
  description,
  items = [],
  checklist = [],
  requiredText = '',
  requiredTextParts = [],
  inputPrompt,
  inputPlaceholder,
  mismatchHint,
  confirmText,
  cancelText,
  destructive = true,
  isLoading = false,
  onConfirm,
  className,
}: RiskAcknowledgementDialogProps) {
  const { t } = useTranslation()
  const [checkedItems, setCheckedItems] = useState<boolean[]>([])
  const [typedText, setTypedText] = useState('')
  const [typedTextParts, setTypedTextParts] = useState<string[]>([])

  const normalizedRequiredTextParts = useMemo<
    NormalizedRequiredTextPart[]
  >(() => {
    return requiredTextParts.reduce<{
      parts: NormalizedRequiredTextPart[]
      inputIndex: number
    }>(
      (acc, part) => {
        if (part.type !== 'input') {
          return { ...acc, parts: [...acc.parts, part] }
        }
        return {
          parts: [...acc.parts, { ...part, inputIndex: acc.inputIndex }],
          inputIndex: acc.inputIndex + 1,
        }
      },
      { parts: [], inputIndex: 0 }
    ).parts
  }, [requiredTextParts])

  const requiredTextInputCount = useMemo(
    () =>
      normalizedRequiredTextParts.filter((part) => part.type === 'input')
        .length,
    [normalizedRequiredTextParts]
  )
  const hasSegmentedRequiredText = requiredTextInputCount > 0
  const requiredTextToDisplay = hasSegmentedRequiredText
    ? normalizedRequiredTextParts.map((part) => part.text).join('')
    : requiredText

  useEffect(() => {
    if (!open) return
    const timer = window.setTimeout(() => {
      setCheckedItems(Array(checklist.length).fill(false))
      setTypedText('')
      setTypedTextParts(Array(requiredTextInputCount).fill(''))
    }, 0)
    return () => window.clearTimeout(timer)
  }, [open, checklist.length, requiredTextInputCount])

  const allChecked = useMemo(() => {
    if (checklist.length === 0) return true
    return (
      checkedItems.length === checklist.length &&
      checkedItems.every((checked) => checked)
    )
  }, [checkedItems, checklist.length])

  const typedMatched = useMemo(() => {
    if (hasSegmentedRequiredText) {
      return normalizedRequiredTextParts.every((part) => {
        if (part.type === 'static') return true
        return typedTextParts[part.inputIndex ?? 0]?.trim() === part.text.trim()
      })
    }
    if (!requiredText) return true
    return typedText.trim() === requiredText.trim()
  }, [
    hasSegmentedRequiredText,
    normalizedRequiredTextParts,
    requiredText,
    typedText,
    typedTextParts,
  ])
  const hasTypedRequiredText = hasSegmentedRequiredText
    ? typedTextParts.some((part) => part.trim() !== '')
    : typedText.length > 0

  const canConfirm = allChecked && typedMatched && !isLoading

  const handleChecklistChange = (index: number, checked: boolean) => {
    setCheckedItems((previous) => {
      const next = [...previous]
      next[index] = checked
      return next
    })
  }

  const handleTextPartChange = (index: number, value: string) => {
    setTypedTextParts((previous) => {
      const next = [...previous]
      next[index] = value
      return next
    })
  }

  return (
    <AlertDialog open={open} onOpenChange={onOpenChange}>
      <AlertDialogContent
        className={cn(
          'flex max-h-[min(88dvh,760px)] w-[calc(100vw-1.5rem)] !max-w-[44rem] grid-rows-none flex-col gap-0 overflow-hidden !p-0 sm:w-[min(44rem,calc(100vw-3rem))]',
          className
        )}
      >
        <AlertDialogHeader className='shrink-0 px-4 pt-4 pb-3 text-left sm:px-6 sm:pt-6'>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {description ? (
            <AlertDialogDescription
              render={<div />}
              className='mt-1 text-left leading-5'
            >
              {description}
            </AlertDialogDescription>
          ) : null}
        </AlertDialogHeader>

        <div className='min-h-0 flex-1 space-y-4 overflow-y-auto px-4 pb-4 sm:px-6'>
          {items.length > 0 ? (
            <ol className='border-border/70 bg-muted/30 text-foreground list-decimal space-y-2 rounded-lg border px-4 py-3 pl-8 text-sm leading-6 sm:px-5 sm:py-4 sm:pl-9'>
              {items.map((item) => (
                <li key={item}>{item}</li>
              ))}
            </ol>
          ) : null}

          {checklist.length > 0 ? (
            <div className='border-border/70 bg-muted/30 space-y-3 rounded-lg border p-3 sm:p-4'>
              {checklist.map((item, index) => {
                const id = `risk-acknowledgement-${index}`
                return (
                  <div key={item} className='flex items-start gap-3'>
                    <Checkbox
                      id={id}
                      checked={checkedItems[index] ?? false}
                      onCheckedChange={(checked) =>
                        handleChecklistChange(index, checked === true)
                      }
                      className='mt-0.5'
                    />
                    <Label
                      htmlFor={id}
                      className='text-muted-foreground text-sm leading-5 font-normal'
                    >
                      {item}
                    </Label>
                  </div>
                )
              })}
            </div>
          ) : null}

          {requiredTextToDisplay ? (
            <div className='border-destructive/30 bg-destructive/5 space-y-3 rounded-lg border p-3 sm:p-4'>
              <Label className='text-sm font-medium'>
                {inputPrompt ?? t('Please type the following text to confirm:')}
              </Label>
              <div className='bg-background border-border rounded-md border px-3 py-2 font-mono text-sm leading-6 break-words whitespace-pre-wrap'>
                {requiredTextToDisplay}
              </div>
              {hasSegmentedRequiredText ? (
                <div className='flex flex-col gap-2'>
                  {normalizedRequiredTextParts.map((part, index) =>
                    part.type === 'static' ? (
                      <span
                        key={`static-${index}`}
                        className='text-muted-foreground bg-background/70 border-border w-fit rounded-md border px-2 py-1.5 font-mono text-sm select-none'
                      >
                        {part.text}
                      </span>
                    ) : (
                      <Textarea
                        key={`input-${index}`}
                        value={typedTextParts[part.inputIndex ?? 0] ?? ''}
                        onChange={(event) =>
                          handleTextPartChange(
                            part.inputIndex ?? 0,
                            event.target.value
                          )
                        }
                        placeholder={
                          part.placeholder ??
                          part.text ??
                          inputPlaceholder ??
                          t('Type the confirmation text here')
                        }
                        rows={getRequiredTextRows(part.text)}
                        autoFocus={open && part.inputIndex === 0}
                        wrap='soft'
                        aria-invalid={hasTypedRequiredText && !typedMatched}
                        className='min-h-10 resize-none overflow-hidden font-mono text-sm leading-6'
                      />
                    )
                  )}
                </div>
              ) : (
                <Textarea
                  value={typedText}
                  onChange={(event) => setTypedText(event.target.value)}
                  placeholder={
                    inputPlaceholder ?? t('Type the confirmation text here')
                  }
                  rows={getRequiredTextRows(requiredText)}
                  autoFocus={open}
                  wrap='soft'
                  aria-invalid={hasTypedRequiredText && !typedMatched}
                  className='min-h-16 resize-none overflow-hidden font-mono text-sm leading-6'
                />
              )}
              {hasTypedRequiredText && !typedMatched ? (
                <p className='text-destructive text-xs'>
                  {mismatchHint ??
                    t('The entered text does not match the required text.')}
                </p>
              ) : null}
            </div>
          ) : null}
        </div>

        <AlertDialogFooter className='mx-0 mb-0 shrink-0 rounded-b-xl border-t p-3 sm:p-4'>
          <AlertDialogCancel disabled={isLoading}>
            {cancelText ?? t('Cancel')}
          </AlertDialogCancel>
          <Button
            variant={destructive ? 'destructive' : 'default'}
            disabled={!canConfirm}
            onClick={onConfirm}
          >
            {confirmText ?? t('Confirm')}
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
