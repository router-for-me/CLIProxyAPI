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

import { ChevronDownIcon } from 'lucide-react'
import {
  type ComponentProps,
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import { Input } from '@/components/ui/input'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import dayjs from '@/lib/dayjs'
import { cn } from '@/lib/utils'

export type WebPreviewContextValue = {
  url: string
  setUrl: (url: string) => void
  consoleOpen: boolean
  setConsoleOpen: (open: boolean) => void
}

const WebPreviewContext = createContext<WebPreviewContextValue | null>(null)

const useWebPreview = () => {
  const context = useContext(WebPreviewContext)
  if (!context) {
    throw new Error('WebPreview components must be used within a WebPreview')
  }
  return context
}

export type WebPreviewProps = ComponentProps<'div'> & {
  defaultUrl?: string
  onUrlChange?: (url: string) => void
}

export const WebPreview = ({
  className,
  children,
  defaultUrl = '',
  onUrlChange,
  ...props
}: WebPreviewProps) => {
  const [url, setUrl] = useState(defaultUrl)
  const [consoleOpen, setConsoleOpen] = useState(false)

  const handleUrlChange = (newUrl: string) => {
    setUrl(newUrl)
    onUrlChange?.(newUrl)
  }

  const contextValue: WebPreviewContextValue = {
    url,
    setUrl: handleUrlChange,
    consoleOpen,
    setConsoleOpen,
  }

  return (
    <WebPreviewContext.Provider value={contextValue}>
      <div
        className={cn(
          'bg-card flex size-full flex-col rounded-lg border',
          className
        )}
        {...props}
      >
        {children}
      </div>
    </WebPreviewContext.Provider>
  )
}

export type WebPreviewNavigationProps = ComponentProps<'div'>

export const WebPreviewNavigation = ({
  className,
  children,
  ...props
}: WebPreviewNavigationProps) => (
  <div
    className={cn('flex items-center gap-1 border-b p-2', className)}
    {...props}
  >
    {children}
  </div>
)

export type WebPreviewNavigationButtonProps = ComponentProps<typeof Button> & {
  tooltip?: string
}

export const WebPreviewNavigationButton = ({
  onClick,
  disabled,
  tooltip,
  children,
  ...props
}: WebPreviewNavigationButtonProps) => (
  <TooltipProvider>
    <Tooltip>
      <TooltipTrigger
        render={
          <Button
            className='hover:text-foreground h-8 w-8 p-0'
            disabled={disabled}
            onClick={onClick}
            size='sm'
            variant='ghost'
            {...props}
          />
        }
      >
        {children}
      </TooltipTrigger>
      <TooltipContent>
        <p>{tooltip}</p>
      </TooltipContent>
    </Tooltip>
  </TooltipProvider>
)

export type WebPreviewUrlProps = ComponentProps<typeof Input>

export const WebPreviewUrl = ({
  value,
  onChange,
  onKeyDown,
  ...props
}: WebPreviewUrlProps) => {
  const { t } = useTranslation()
  const { url, setUrl } = useWebPreview()
  const [inputValue, setInputValue] = useState(url)

  // Sync input value with context URL when it changes externally
  useEffect(() => {
    setInputValue(url)
  }, [url])

  const handleChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    setInputValue(event.target.value)
    onChange?.(event)
  }

  const handleKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    if (event.key === 'Enter') {
      const target = event.target as HTMLInputElement
      setUrl(target.value)
    }
    onKeyDown?.(event)
  }

  return (
    <Input
      className='h-8 flex-1 text-sm'
      onChange={onChange ?? handleChange}
      onKeyDown={handleKeyDown}
      placeholder={t('Enter URL...')}
      value={value ?? inputValue}
      {...props}
    />
  )
}

export type WebPreviewBodyProps = ComponentProps<'iframe'> & {
  loading?: ReactNode
}

export const WebPreviewBody = ({
  className,
  loading,
  src,
  ...props
}: WebPreviewBodyProps) => {
  const { t } = useTranslation()
  const { url } = useWebPreview()

  return (
    <div className='flex-1'>
      <iframe
        className={cn('size-full', className)}
        sandbox='allow-scripts allow-same-origin allow-forms allow-popups allow-presentation'
        src={(src ?? url) || undefined}
        title={t('Preview')}
        {...props}
      />
      {loading}
    </div>
  )
}

export type WebPreviewConsoleProps = ComponentProps<'div'> & {
  logs?: Array<{
    level: 'log' | 'warn' | 'error'
    message: string
    timestamp: Date
  }>
}

export const WebPreviewConsole = ({
  className,
  logs = [],
  children,
  ...props
}: WebPreviewConsoleProps) => {
  const { t } = useTranslation()
  const { consoleOpen, setConsoleOpen } = useWebPreview()

  return (
    <Collapsible
      className={cn('bg-muted/50 border-t font-mono text-sm', className)}
      onOpenChange={setConsoleOpen}
      open={consoleOpen}
      {...props}
    >
      <CollapsibleTrigger
        render={
          <Button
            className='hover:bg-muted/50 flex w-full items-center justify-between p-4 text-left font-medium'
            variant='ghost'
          />
        }
      >
        {t('Console')}
        <ChevronDownIcon
          className={cn(
            'h-4 w-4 transition-transform duration-200',
            consoleOpen && 'rotate-180'
          )}
        />
      </CollapsibleTrigger>
      <CollapsibleContent
        className={cn(
          'px-4 pb-4',
          'data-closed:fade-out-0 data-open:fade-in-0 data-closed:zoom-out-95 data-open:zoom-in-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 data-closed:animate-out data-open:animate-in outline-none'
        )}
      >
        <div className='max-h-48 space-y-1 overflow-y-auto'>
          {logs.length === 0 ? (
            <p className='text-muted-foreground'>{t('No console output')}</p>
          ) : (
            logs.map((log, index) => (
              <div
                className={cn(
                  'text-xs',
                  log.level === 'error' && 'text-destructive',
                  log.level === 'warn' && 'text-warning',
                  log.level === 'log' && 'text-foreground'
                )}
                key={`${log.timestamp.getTime()}-${index}`}
              >
                <span className='text-muted-foreground'>
                  {dayjs(log.timestamp).format('HH:mm:ss')}
                </span>{' '}
                {log.message}
              </div>
            ))
          )}
          {children}
        </div>
      </CollapsibleContent>
    </Collapsible>
  )
}
