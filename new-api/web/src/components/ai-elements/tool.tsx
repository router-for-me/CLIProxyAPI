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

import type { ToolUIPart } from 'ai'
import {
  CheckCircleIcon,
  ChevronDownIcon,
  CircleIcon,
  ClockIcon,
  WrenchIcon,
  XCircleIcon,
} from 'lucide-react'
import { type ComponentProps, isValidElement, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

import { CodeBlock } from './code-block'

// Workaround for missing types in 'ai' package
type ExtendedToolState =
  | ToolUIPart['state']
  | 'approval-requested'
  | 'approval-responded'
  | 'output-denied'

export type ToolProps = ComponentProps<typeof Collapsible>

export const Tool = ({ className, ...props }: ToolProps) => (
  <Collapsible
    className={cn('not-prose mb-4 w-full rounded-md border', className)}
    {...props}
  />
)

export type ToolHeaderProps = {
  title?: string
  type: ToolUIPart['type']
  state: ExtendedToolState
  className?: string
}

const getStatusBadge = (status: ExtendedToolState) => {
  const labels: Record<ExtendedToolState, string> = {
    'input-streaming': 'Pending',
    'input-available': 'Running',
    'approval-requested': 'Awaiting Approval',
    'approval-responded': 'Responded',
    'output-available': 'Completed',
    'output-error': 'Error',
    'output-denied': 'Denied',
  }

  const icons: Record<ExtendedToolState, ReactNode> = {
    'input-streaming': <CircleIcon className='size-4' />,
    'input-available': <ClockIcon className='size-4 animate-pulse' />,
    'approval-requested': <ClockIcon className='text-warning size-4' />,
    'approval-responded': <CheckCircleIcon className='text-info size-4' />,
    'output-available': <CheckCircleIcon className='text-success size-4' />,
    'output-error': <XCircleIcon className='text-destructive size-4' />,
    'output-denied': <XCircleIcon className='text-warning size-4' />,
  }

  return (
    <Badge className='gap-1.5 text-xs' variant='secondary'>
      {icons[status]}
      {labels[status]}
    </Badge>
  )
}

export const ToolHeader = ({
  className,
  title,
  type,
  state,
  ...props
}: ToolHeaderProps) => (
  <CollapsibleTrigger
    className={cn(
      'group flex w-full items-center justify-between gap-4 p-3',
      className
    )}
    {...props}
  >
    <div className='flex items-center gap-2'>
      <WrenchIcon className='text-muted-foreground size-4' />
      <span className='text-sm font-medium'>
        {title ?? type.split('-').slice(1).join('-')}
      </span>
      {getStatusBadge(state)}
    </div>
    <ChevronDownIcon className='text-muted-foreground size-4 transition-transform group-data-[panel-open]:rotate-180' />
  </CollapsibleTrigger>
)

export type ToolContentProps = ComponentProps<typeof CollapsibleContent>

export const ToolContent = ({ className, ...props }: ToolContentProps) => (
  <CollapsibleContent
    className={cn(
      'data-closed:fade-out-0 data-closed:slide-out-to-top-2 data-open:slide-in-from-top-2 text-popover-foreground data-closed:animate-out data-open:animate-in outline-none',
      className
    )}
    {...props}
  />
)

export type ToolInputProps = ComponentProps<'div'> & {
  input: ToolUIPart['input']
}

export const ToolInput = ({ className, input, ...props }: ToolInputProps) => {
  const { t } = useTranslation()
  return (
    <div className={cn('space-y-2 overflow-hidden p-4', className)} {...props}>
      <h4 className='text-muted-foreground text-xs font-medium tracking-wide uppercase'>
        {t('Parameters')}
      </h4>
      <div className='bg-muted/50 rounded-md'>
        <CodeBlock code={JSON.stringify(input, null, 2)} language='json' />
      </div>
    </div>
  )
}

export type ToolOutputProps = ComponentProps<'div'> & {
  output: ToolUIPart['output']
  errorText: ToolUIPart['errorText']
}

export const ToolOutput = ({
  className,
  output,
  errorText,
  ...props
}: ToolOutputProps) => {
  if (!(output || errorText)) {
    return null
  }

  let Output = <div>{output as ReactNode}</div>

  if (typeof output === 'object' && !isValidElement(output)) {
    Output = (
      <CodeBlock code={JSON.stringify(output, null, 2)} language='json' />
    )
  } else if (typeof output === 'string') {
    Output = <CodeBlock code={output} language='json' />
  }

  return (
    <div className={cn('space-y-2 p-4', className)} {...props}>
      <h4 className='text-muted-foreground text-xs font-medium tracking-wide uppercase'>
        {errorText ? 'Error' : 'Result'}
      </h4>
      <div
        className={cn(
          'overflow-x-auto rounded-md text-xs [&_table]:w-full',
          errorText
            ? 'bg-destructive/10 text-destructive'
            : 'bg-muted/50 text-foreground'
        )}
      >
        {errorText && <div>{errorText}</div>}
        {Output}
      </div>
    </div>
  )
}
