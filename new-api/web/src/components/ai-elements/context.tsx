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

import type { LanguageModelUsage } from 'ai'
import { type ComponentProps, createContext, useContext } from 'react'
import { useTranslation } from 'react-i18next'
import { getUsage } from 'tokenlens'

import { Button } from '@/components/ui/button'
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from '@/components/ui/hover-card'
import { Progress } from '@/components/ui/progress'
import { cn } from '@/lib/utils'

const PERCENT_MAX = 100
const ICON_RADIUS = 10
const ICON_VIEWBOX = 24
const ICON_CENTER = 12
const ICON_STROKE_WIDTH = 2

type ModelId = string

type ContextSchema = {
  usedTokens: number
  maxTokens: number
  usage?: LanguageModelUsage
  modelId?: ModelId
}

const ContextContext = createContext<ContextSchema | null>(null)

const useContextValue = () => {
  const context = useContext(ContextContext)

  if (!context) {
    throw new Error('Context components must be used within Context')
  }

  return context
}

export type ContextProps = ComponentProps<typeof HoverCard> & ContextSchema

export const Context = ({
  usedTokens,
  maxTokens,
  usage,
  modelId,
  ...props
}: ContextProps) => (
  <ContextContext.Provider
    value={{
      usedTokens,
      maxTokens,
      usage,
      modelId,
    }}
  >
    <HoverCard {...props} />
  </ContextContext.Provider>
)

const ContextIcon = () => {
  const { t } = useTranslation()
  const { usedTokens, maxTokens } = useContextValue()
  const circumference = 2 * Math.PI * ICON_RADIUS
  const usedPercent = usedTokens / maxTokens
  const dashOffset = circumference * (1 - usedPercent)

  return (
    <svg
      aria-label={t('Model context usage')}
      height='20'
      role='img'
      style={{ color: 'currentcolor' }}
      viewBox={`0 0 ${ICON_VIEWBOX} ${ICON_VIEWBOX}`}
      width='20'
    >
      <circle
        cx={ICON_CENTER}
        cy={ICON_CENTER}
        fill='none'
        opacity='0.25'
        r={ICON_RADIUS}
        stroke='currentColor'
        strokeWidth={ICON_STROKE_WIDTH}
      />
      <circle
        cx={ICON_CENTER}
        cy={ICON_CENTER}
        fill='none'
        opacity='0.7'
        r={ICON_RADIUS}
        stroke='currentColor'
        strokeDasharray={`${circumference} ${circumference}`}
        strokeDashoffset={dashOffset}
        strokeLinecap='round'
        strokeWidth={ICON_STROKE_WIDTH}
        style={{ transformOrigin: 'center', transform: 'rotate(-90deg)' }}
      />
    </svg>
  )
}

export type ContextTriggerProps = ComponentProps<typeof Button>

export const ContextTrigger = ({ children, ...props }: ContextTriggerProps) => {
  const { usedTokens, maxTokens } = useContextValue()
  const usedPercent = usedTokens / maxTokens
  const renderedPercent = new Intl.NumberFormat('en-US', {
    style: 'percent',
    maximumFractionDigits: 1,
  }).format(usedPercent)

  return (
    <HoverCardTrigger
      delay={0}
      closeDelay={0}
      render={
        <Button type='button' variant='ghost' {...props}>
          {children ?? (
            <>
              <span className='text-muted-foreground font-medium'>
                {renderedPercent}
              </span>
              <ContextIcon />
            </>
          )}
        </Button>
      }
    />
  )
}

export type ContextContentProps = ComponentProps<typeof HoverCardContent>

export const ContextContent = ({
  className,
  ...props
}: ContextContentProps) => (
  <HoverCardContent
    className={cn('min-w-60 divide-y overflow-hidden p-0', className)}
    {...props}
  />
)

export type ContextContentHeaderProps = ComponentProps<'div'>

export const ContextContentHeader = ({
  children,
  className,
  ...props
}: ContextContentHeaderProps) => {
  const { usedTokens, maxTokens } = useContextValue()
  const usedPercent = usedTokens / maxTokens
  const displayPct = new Intl.NumberFormat('en-US', {
    style: 'percent',
    maximumFractionDigits: 1,
  }).format(usedPercent)
  const used = new Intl.NumberFormat('en-US', {
    notation: 'compact',
  }).format(usedTokens)
  const total = new Intl.NumberFormat('en-US', {
    notation: 'compact',
  }).format(maxTokens)

  return (
    <div className={cn('w-full space-y-2 p-3', className)} {...props}>
      {children ?? (
        <>
          <div className='flex items-center justify-between gap-3 text-xs'>
            <p>{displayPct}</p>
            <p className='text-muted-foreground font-mono'>
              {used} / {total}
            </p>
          </div>
          <div className='space-y-2'>
            <Progress className='bg-muted' value={usedPercent * PERCENT_MAX} />
          </div>
        </>
      )}
    </div>
  )
}

export type ContextContentBodyProps = ComponentProps<'div'>

export const ContextContentBody = ({
  children,
  className,
  ...props
}: ContextContentBodyProps) => (
  <div className={cn('w-full p-3', className)} {...props}>
    {children}
  </div>
)

export type ContextContentFooterProps = ComponentProps<'div'>

export const ContextContentFooter = ({
  children,
  className,
  ...props
}: ContextContentFooterProps) => {
  const { t } = useTranslation()
  const { modelId, usage } = useContextValue()
  const costUSD = modelId
    ? getUsage({
        modelId,
        usage: {
          input: usage?.inputTokens ?? 0,
          output: usage?.outputTokens ?? 0,
        },
      }).costUSD?.totalUSD
    : undefined
  const totalCost = new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
  }).format(costUSD ?? 0)

  return (
    <div
      className={cn(
        'bg-secondary flex w-full items-center justify-between gap-3 p-3 text-xs',
        className
      )}
      {...props}
    >
      {children ?? (
        <>
          <span className='text-muted-foreground'>{t('Total cost')}</span>
          <span>{totalCost}</span>
        </>
      )}
    </div>
  )
}

export type ContextInputUsageProps = ComponentProps<'div'>

export const ContextInputUsage = ({
  className,
  children,
  ...props
}: ContextInputUsageProps) => {
  const { t } = useTranslation()
  const { usage, modelId } = useContextValue()
  const inputTokens = usage?.inputTokens ?? 0

  if (children) {
    return children
  }

  if (!inputTokens) {
    return null
  }

  const inputCost = modelId
    ? getUsage({
        modelId,
        usage: { input: inputTokens, output: 0 },
      }).costUSD?.totalUSD
    : undefined
  const inputCostText = new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
  }).format(inputCost ?? 0)

  return (
    <div
      className={cn('flex items-center justify-between text-xs', className)}
      {...props}
    >
      <span className='text-muted-foreground'>{t('Input')}</span>
      <TokensWithCost costText={inputCostText} tokens={inputTokens} />
    </div>
  )
}

export type ContextOutputUsageProps = ComponentProps<'div'>

export const ContextOutputUsage = ({
  className,
  children,
  ...props
}: ContextOutputUsageProps) => {
  const { t } = useTranslation()
  const { usage, modelId } = useContextValue()
  const outputTokens = usage?.outputTokens ?? 0

  if (children) {
    return children
  }

  if (!outputTokens) {
    return null
  }

  const outputCost = modelId
    ? getUsage({
        modelId,
        usage: { input: 0, output: outputTokens },
      }).costUSD?.totalUSD
    : undefined
  const outputCostText = new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
  }).format(outputCost ?? 0)

  return (
    <div
      className={cn('flex items-center justify-between text-xs', className)}
      {...props}
    >
      <span className='text-muted-foreground'>{t('Output')}</span>
      <TokensWithCost costText={outputCostText} tokens={outputTokens} />
    </div>
  )
}

export type ContextReasoningUsageProps = ComponentProps<'div'>

export const ContextReasoningUsage = ({
  className,
  children,
  ...props
}: ContextReasoningUsageProps) => {
  const { t } = useTranslation()
  const { usage, modelId } = useContextValue()
  const reasoningTokens = usage?.outputTokenDetails.reasoningTokens ?? 0

  if (children) {
    return children
  }

  if (!reasoningTokens) {
    return null
  }

  const reasoningCost = modelId
    ? getUsage({
        modelId,
        usage: { reasoningTokens },
      }).costUSD?.totalUSD
    : undefined
  const reasoningCostText = new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
  }).format(reasoningCost ?? 0)

  return (
    <div
      className={cn('flex items-center justify-between text-xs', className)}
      {...props}
    >
      <span className='text-muted-foreground'>{t('Reasoning')}</span>
      <TokensWithCost costText={reasoningCostText} tokens={reasoningTokens} />
    </div>
  )
}

export type ContextCacheUsageProps = ComponentProps<'div'>

export const ContextCacheUsage = ({
  className,
  children,
  ...props
}: ContextCacheUsageProps) => {
  const { t } = useTranslation()
  const { usage, modelId } = useContextValue()
  const cacheTokens = usage?.inputTokenDetails.cacheReadTokens ?? 0

  if (children) {
    return children
  }

  if (!cacheTokens) {
    return null
  }

  const cacheCost = modelId
    ? getUsage({
        modelId,
        usage: { cacheReads: cacheTokens, input: 0, output: 0 },
      }).costUSD?.totalUSD
    : undefined
  const cacheCostText = new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
  }).format(cacheCost ?? 0)

  return (
    <div
      className={cn('flex items-center justify-between text-xs', className)}
      {...props}
    >
      <span className='text-muted-foreground'>{t('Cache')}</span>
      <TokensWithCost costText={cacheCostText} tokens={cacheTokens} />
    </div>
  )
}

const TokensWithCost = ({
  tokens,
  costText,
}: {
  tokens?: number
  costText?: string
}) => (
  <span>
    {tokens === undefined
      ? '—'
      : new Intl.NumberFormat('en-US', {
          notation: 'compact',
        }).format(tokens)}
    {costText ? (
      <span className='text-muted-foreground ml-2'>• {costText}</span>
    ) : null}
  </span>
)
