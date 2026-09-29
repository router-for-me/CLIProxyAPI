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

import { ChevronsUpDownIcon } from 'lucide-react'
import { type ComponentProps, createContext, useContext } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

import { Shimmer } from './shimmer'

type PlanContextValue = {
  isStreaming: boolean
}

const PlanContext = createContext<PlanContextValue | null>(null)

const usePlan = () => {
  const context = useContext(PlanContext)
  if (!context) {
    throw new Error('Plan components must be used within Plan')
  }
  return context
}

export type PlanProps = ComponentProps<typeof Collapsible> & {
  isStreaming?: boolean
}

export const Plan = ({
  className,
  isStreaming = false,
  children,
  ...props
}: PlanProps) => (
  <PlanContext.Provider value={{ isStreaming }}>
    <Collapsible
      data-slot='plan'
      {...props}
      render={<Card className={cn('shadow-none', className)} />}
    >
      {children}
    </Collapsible>
  </PlanContext.Provider>
)

export type PlanHeaderProps = ComponentProps<typeof CardHeader>

export const PlanHeader = ({ className, ...props }: PlanHeaderProps) => (
  <CardHeader
    className={cn('flex items-start justify-between', className)}
    data-slot='plan-header'
    {...props}
  />
)

export type PlanTitleProps = Omit<
  ComponentProps<typeof CardTitle>,
  'children'
> & {
  children: string
}

export const PlanTitle = ({ children, ...props }: PlanTitleProps) => {
  const { isStreaming } = usePlan()

  return (
    <CardTitle data-slot='plan-title' {...props}>
      {isStreaming ? <Shimmer>{children}</Shimmer> : children}
    </CardTitle>
  )
}

export type PlanDescriptionProps = Omit<
  ComponentProps<typeof CardDescription>,
  'children'
> & {
  children: string
}

export const PlanDescription = ({
  className,
  children,
  ...props
}: PlanDescriptionProps) => {
  const { isStreaming } = usePlan()

  return (
    <CardDescription
      className={cn('text-balance', className)}
      data-slot='plan-description'
      {...props}
    >
      {isStreaming ? <Shimmer>{children}</Shimmer> : children}
    </CardDescription>
  )
}

export type PlanActionProps = ComponentProps<typeof CardAction>

export const PlanAction = (props: PlanActionProps) => (
  <CardAction data-slot='plan-action' {...props} />
)

export type PlanContentProps = ComponentProps<typeof CardContent>

export const PlanContent = (props: PlanContentProps) => (
  <CollapsibleContent
    render={<CardContent data-slot='plan-content' {...props} />}
  ></CollapsibleContent>
)

export type PlanFooterProps = ComponentProps<'div'>

export const PlanFooter = (props: PlanFooterProps) => (
  <CardFooter data-slot='plan-footer' {...props} />
)

export type PlanTriggerProps = ComponentProps<typeof CollapsibleTrigger>

export const PlanTrigger = ({ className, ...props }: PlanTriggerProps) => {
  const { t } = useTranslation()
  return (
    <CollapsibleTrigger
      render={
        <Button
          className={cn('size-8', className)}
          data-slot='plan-trigger'
          size='icon'
          variant='ghost'
        />
      }
      {...props}
    >
      <ChevronsUpDownIcon className='size-4' />
      <span className='sr-only'>{t('Toggle plan')}</span>
    </CollapsibleTrigger>
  )
}
