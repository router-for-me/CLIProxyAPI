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
import { CheckCircle2, Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

interface StepNavigationProps {
  currentStep: number
  totalSteps: number
  onBack: () => void
  onNext: () => void
  onSubmit: () => void
  isSubmitting?: boolean
}

export function StepNavigation({
  currentStep,
  totalSteps,
  onBack,
  onNext,
  onSubmit,
  isSubmitting = false,
}: StepNavigationProps) {
  const { t } = useTranslation()
  const isFirstStep = currentStep === 0
  const isLastStep = currentStep === totalSteps - 1

  return (
    <div className='flex w-full flex-col gap-3 sm:flex-row sm:items-center'>
      <div className='flex justify-end gap-2 sm:justify-start'>
        {!isFirstStep && (
          <Button type='button' variant='outline' onClick={onBack}>
            {t('Back')}
          </Button>
        )}
      </div>

      <div className='flex flex-1 justify-end gap-2'>
        {!isLastStep && (
          <Button type='button' onClick={onNext}>
            {t('Next')}
          </Button>
        )}

        {isLastStep && (
          <Button type='button' onClick={onSubmit} disabled={isSubmitting}>
            {isSubmitting ? (
              <>
                <Loader2 className='mr-2 size-4 animate-spin' />
                {t('Initializing…')}
              </>
            ) : (
              <>
                <CheckCircle2 className='mr-2 size-4' />
                {t('Initialize system')}
              </>
            )}
          </Button>
        )}
      </div>
    </div>
  )
}
