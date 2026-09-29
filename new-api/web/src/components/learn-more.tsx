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
import { CircleQuestionMark } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { cn } from '@/lib/utils'

type LearnMoreProps = Omit<React.ComponentProps<typeof Popover>, 'children'> & {
  children?: React.ReactNode
  contentProps?: React.ComponentProps<typeof PopoverContent>
  triggerProps?: React.ComponentProps<typeof PopoverTrigger>
}

export function LearnMore({
  children,
  contentProps,
  triggerProps,
  ...props
}: LearnMoreProps) {
  const { t } = useTranslation()
  return (
    <Popover {...props}>
      <PopoverTrigger
        {...triggerProps}
        className={cn('size-5', triggerProps?.className)}
        render={<Button variant='outline' size='icon' />}
      >
        <span className='sr-only'>{t('Learn more')}</span>
        <CircleQuestionMark className='size-4 [&>circle]:hidden' />
      </PopoverTrigger>
      <PopoverContent
        side='top'
        align='start'
        {...contentProps}
        className={cn('text-muted-foreground text-sm', contentProps?.className)}
      >
        {children}
      </PopoverContent>
    </Popover>
  )
}
