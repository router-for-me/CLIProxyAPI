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
import { Building2, Home, Presentation } from 'lucide-react'
import type { ComponentType } from 'react'
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import {
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { cn } from '@/lib/utils'

import type { SetupFormValues, SetupUsageMode } from '../types'

interface UsageModeStepProps {
  form: UseFormReturn<SetupFormValues>
}

const USAGE_MODE_OPTIONS: Array<{
  value: SetupUsageMode
  titleKey: string
  descriptionKey: string
  icon: ComponentType<{ className?: string }>
}> = [
  {
    value: 'external',
    titleKey: 'External operations',
    descriptionKey:
      'Serve multiple users or teams with billing and quota control.',
    icon: Building2,
  },
  {
    value: 'self',
    titleKey: 'Personal use',
    descriptionKey:
      'Best for single-tenant deployments. Pricing and billing options stay hidden.',
    icon: Home,
  },
  {
    value: 'demo',
    titleKey: 'Demo site',
    descriptionKey:
      'Showcase core capabilities with demo credentials and limited access.',
    icon: Presentation,
  },
]

export function UsageModeStep({ form }: UsageModeStepProps) {
  const { t } = useTranslation()

  return (
    <FormField
      control={form.control}
      name='usageMode'
      render={({ field }) => (
        <FormItem>
          <FormLabel>{t('How will you use the platform?')}</FormLabel>
          <FormControl>
            <RadioGroup
              value={field.value}
              onValueChange={(value) => {
                form.clearErrors('usageMode')
                field.onChange(value as SetupUsageMode)
              }}
              className='grid gap-3 sm:grid-cols-3'
            >
              {USAGE_MODE_OPTIONS.map(
                ({ value, titleKey, descriptionKey, icon: Icon }) => {
                  return (
                    <Label
                      key={value}
                      htmlFor={`usage-mode-${value}`}
                      className={cn(
                        'hover:border-primary/40 focus-within:border-primary/50 has-data-[checked]:border-primary has-data-[checked]:ring-primary/20 group bg-card border-muted flex cursor-pointer flex-col gap-3 rounded-xl border p-4 font-normal transition-all has-data-[checked]:ring-2'
                      )}
                    >
                      <div className='flex items-center gap-3'>
                        <RadioGroupItem
                          id={`usage-mode-${value}`}
                          value={value}
                          className='mt-1'
                        />
                        <div>
                          <Label
                            htmlFor={`usage-mode-${value}`}
                            className='text-base leading-none font-semibold'
                          >
                            {t(titleKey)}
                          </Label>
                          <p className='text-muted-foreground mt-2 text-sm'>
                            {t(descriptionKey)}
                          </p>
                        </div>
                        <Icon className='text-muted-foreground/70 group-hover:text-primary group-focus-within:text-primary group-has-data-[checked]:text-primary ml-auto size-5 shrink-0 transition' />
                      </div>
                    </Label>
                  )
                }
              )}
            </RadioGroup>
          </FormControl>
          <FormMessage />
        </FormItem>
      )}
    />
  )
}
