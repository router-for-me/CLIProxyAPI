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
'use client'

import { type LucideIcon, XIcon } from 'lucide-react'
import type { ComponentProps, HTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

export type ArtifactProps = HTMLAttributes<HTMLDivElement>

export const Artifact = ({ className, ...props }: ArtifactProps) => (
  <div
    className={cn(
      'bg-background flex flex-col overflow-hidden rounded-lg border shadow-sm',
      className
    )}
    {...props}
  />
)

export type ArtifactHeaderProps = HTMLAttributes<HTMLDivElement>

export const ArtifactHeader = ({
  className,
  ...props
}: ArtifactHeaderProps) => (
  <div
    className={cn(
      'bg-muted/50 flex items-center justify-between border-b px-4 py-3',
      className
    )}
    {...props}
  />
)

export type ArtifactCloseProps = ComponentProps<typeof Button>

export const ArtifactClose = ({
  className,
  children,
  size = 'sm',
  variant = 'ghost',
  ...props
}: ArtifactCloseProps) => {
  const { t } = useTranslation()
  return (
    <Button
      className={cn(
        'text-muted-foreground hover:text-foreground size-8 p-0',
        className
      )}
      size={size}
      type='button'
      variant={variant}
      {...props}
    >
      {children ?? <XIcon className='size-4' />}
      <span className='sr-only'>{t('Close')}</span>
    </Button>
  )
}

export type ArtifactTitleProps = HTMLAttributes<HTMLParagraphElement>

export const ArtifactTitle = ({ className, ...props }: ArtifactTitleProps) => (
  <p
    className={cn('text-foreground text-sm font-medium', className)}
    {...props}
  />
)

export type ArtifactDescriptionProps = HTMLAttributes<HTMLParagraphElement>

export const ArtifactDescription = ({
  className,
  ...props
}: ArtifactDescriptionProps) => (
  <p className={cn('text-muted-foreground text-sm', className)} {...props} />
)

export type ArtifactActionsProps = HTMLAttributes<HTMLDivElement>

export const ArtifactActions = ({
  className,
  ...props
}: ArtifactActionsProps) => (
  <div className={cn('flex items-center gap-1', className)} {...props} />
)

export type ArtifactActionProps = ComponentProps<typeof Button> & {
  tooltip?: string
  label?: string
  icon?: LucideIcon
}

export const ArtifactAction = ({
  tooltip,
  label,
  icon: Icon,
  children,
  className,
  size = 'sm',
  variant = 'ghost',
  ...props
}: ArtifactActionProps) => {
  const button = (
    <Button
      className={cn(
        'text-muted-foreground hover:text-foreground size-8 p-0',
        className
      )}
      size={size}
      type='button'
      variant={variant}
      {...props}
    >
      {Icon ? <Icon className='size-4' /> : children}
      <span className='sr-only'>{label || tooltip}</span>
    </Button>
  )

  if (tooltip) {
    return (
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger render={button}></TooltipTrigger>
          <TooltipContent>
            <p>{tooltip}</p>
          </TooltipContent>
        </Tooltip>
      </TooltipProvider>
    )
  }

  return button
}

export type ArtifactContentProps = HTMLAttributes<HTMLDivElement>

export const ArtifactContent = ({
  className,
  ...props
}: ArtifactContentProps) => (
  <div className={cn('flex-1 overflow-auto p-4', className)} {...props} />
)
