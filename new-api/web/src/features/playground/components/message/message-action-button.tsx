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
import type { LucideIcon } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

import { MESSAGE_ACTION_BUTTON_STYLES } from '../../constants'

interface MessageActionButtonProps {
  icon: LucideIcon
  label: string
  onClick: () => void
  disabled?: boolean
  className?: string
  variant?: 'default' | 'destructive'
}

export function MessageActionButton({
  icon: Icon,
  label,
  onClick,
  disabled = false,
  className = '',
  variant = 'default',
}: MessageActionButtonProps) {
  const baseStyle =
    variant === 'destructive'
      ? MESSAGE_ACTION_BUTTON_STYLES.DELETE
      : MESSAGE_ACTION_BUTTON_STYLES.BASE

  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            variant='ghost'
            size='icon'
            className={`${baseStyle} ${className}`}
            onClick={onClick}
            disabled={disabled}
            aria-label={label}
          />
        }
      >
        <Icon className={MESSAGE_ACTION_BUTTON_STYLES.ICON} />
      </TooltipTrigger>
      <TooltipContent>
        <p>{label}</p>
      </TooltipContent>
    </Tooltip>
  )
}
