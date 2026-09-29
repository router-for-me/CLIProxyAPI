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
import { ChevronDown, Settings } from 'lucide-react'
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
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

type ChannelAdvancedSectionProps = {
  children: ReactNode
  open: boolean
  onOpenChange: (open: boolean) => void
  summary?: ReactNode
}

export function ChannelAdvancedSection(props: ChannelAdvancedSectionProps) {
  const { t } = useTranslation()

  return (
    <Collapsible open={props.open} onOpenChange={props.onOpenChange}>
      <CollapsibleTrigger
        render={
          <button
            type='button'
            className='hover:bg-muted/40 border-border/60 flex w-full items-center justify-between rounded-lg border px-3 py-3 text-left transition-colors'
            aria-expanded={props.open}
          />
        }
      >
        <div className='flex items-start gap-3'>
          <span className='bg-muted text-muted-foreground flex size-8 shrink-0 items-center justify-center rounded-md'>
            <Settings className='h-4 w-4' aria-hidden='true' />
          </span>
          <div className='flex flex-col gap-0.5'>
            <div className='text-[13px] font-semibold'>
              {t('Advanced Settings')}
            </div>
            <div className='text-muted-foreground text-xs'>
              {props.summary ??
                t(
                  'Request overrides, routing behavior, and upstream model automation'
                )}
            </div>
          </div>
        </div>
        <ChevronDown
          className={cn(
            'text-muted-foreground h-4 w-4 shrink-0 transition-transform',
            props.open && 'rotate-180'
          )}
          aria-hidden='true'
        />
      </CollapsibleTrigger>

      <CollapsibleContent className='mt-5 flex flex-col gap-5'>
        {props.children}
      </CollapsibleContent>
    </Collapsible>
  )
}
