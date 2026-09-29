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

import { ArrowLeftIcon, ArrowRightIcon } from 'lucide-react'
import {
  type ComponentProps,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Carousel,
  type CarouselApi,
  CarouselContent,
  CarouselItem,
} from '@/components/ui/carousel'
import {
  HoverCard,
  HoverCardContent,
  HoverCardTrigger,
} from '@/components/ui/hover-card'
import { cn } from '@/lib/utils'

export type InlineCitationProps = ComponentProps<'span'>

export const InlineCitation = ({
  className,
  ...props
}: InlineCitationProps) => (
  <span
    className={cn('group inline items-center gap-1', className)}
    {...props}
  />
)

export type InlineCitationTextProps = ComponentProps<'span'>

export const InlineCitationText = ({
  className,
  ...props
}: InlineCitationTextProps) => (
  <span
    className={cn('group-hover:bg-accent transition-colors', className)}
    {...props}
  />
)

export type InlineCitationCardProps = ComponentProps<typeof HoverCard>

export const InlineCitationCard = (props: InlineCitationCardProps) => (
  <HoverCard {...props} />
)

export type InlineCitationCardTriggerProps = ComponentProps<typeof Badge> & {
  sources: string[]
}

export const InlineCitationCardTrigger = ({
  sources,
  className,
  ...props
}: InlineCitationCardTriggerProps) => (
  <HoverCardTrigger
    delay={0}
    closeDelay={0}
    render={
      <Badge className={cn('ml-1', className)} variant='secondary' {...props}>
        {sources[0] ? (
          <>
            {new URL(sources[0]).hostname}{' '}
            {sources.length > 1 && `+${sources.length - 1}`}
          </>
        ) : (
          'unknown'
        )}
      </Badge>
    }
  />
)

export type InlineCitationCardBodyProps = ComponentProps<'div'>

export const InlineCitationCardBody = ({
  className,
  ...props
}: InlineCitationCardBodyProps) => (
  <HoverCardContent className={cn('relative w-80 p-0', className)} {...props} />
)

const CarouselApiContext = createContext<CarouselApi | undefined>(undefined)

const useCarouselApi = () => {
  const context = useContext(CarouselApiContext)
  return context
}

export type InlineCitationCarouselProps = ComponentProps<typeof Carousel>

export const InlineCitationCarousel = ({
  className,
  children,
  ...props
}: InlineCitationCarouselProps) => {
  const [api, setApi] = useState<CarouselApi>()

  return (
    <CarouselApiContext.Provider value={api}>
      <Carousel className={cn('w-full', className)} setApi={setApi} {...props}>
        {children}
      </Carousel>
    </CarouselApiContext.Provider>
  )
}

export type InlineCitationCarouselContentProps = ComponentProps<'div'>

export const InlineCitationCarouselContent = (
  props: InlineCitationCarouselContentProps
) => <CarouselContent {...props} />

export type InlineCitationCarouselItemProps = ComponentProps<'div'>

export const InlineCitationCarouselItem = ({
  className,
  ...props
}: InlineCitationCarouselItemProps) => (
  <CarouselItem
    className={cn('w-full space-y-2 p-4 pl-8', className)}
    {...props}
  />
)

export type InlineCitationCarouselHeaderProps = ComponentProps<'div'>

export const InlineCitationCarouselHeader = ({
  className,
  ...props
}: InlineCitationCarouselHeaderProps) => (
  <div
    className={cn(
      'bg-secondary flex items-center justify-between gap-2 rounded-t-md p-2',
      className
    )}
    {...props}
  />
)

export type InlineCitationCarouselIndexProps = ComponentProps<'div'>

export const InlineCitationCarouselIndex = ({
  children,
  className,
  ...props
}: InlineCitationCarouselIndexProps) => {
  const api = useCarouselApi()
  const [current, setCurrent] = useState(0)
  const [count, setCount] = useState(0)

  useEffect(() => {
    if (!api) {
      return
    }

    // eslint-disable-next-line react-hooks/set-state-in-effect
    setCount(api.scrollSnapList().length)

    setCurrent(api.selectedScrollSnap() + 1)

    api.on('select', () => {
      setCurrent(api.selectedScrollSnap() + 1)
    })
  }, [api])

  return (
    <div
      className={cn(
        'text-muted-foreground flex flex-1 items-center justify-end px-3 py-1 text-xs',
        className
      )}
      {...props}
    >
      {children ?? `${current}/${count}`}
    </div>
  )
}

export type InlineCitationCarouselPrevProps = ComponentProps<'button'>

export const InlineCitationCarouselPrev = ({
  className,
  ...props
}: InlineCitationCarouselPrevProps) => {
  const { t } = useTranslation()
  const api = useCarouselApi()

  const handleClick = useCallback(() => {
    if (api) {
      api.scrollPrev()
    }
  }, [api])

  return (
    <Button
      variant='ghost'
      size='icon-sm'
      aria-label={t('Previous')}
      className={cn('shrink-0', className)}
      onClick={handleClick}
      type='button'
      {...props}
    >
      <ArrowLeftIcon className='text-muted-foreground size-4' />
    </Button>
  )
}

export type InlineCitationCarouselNextProps = ComponentProps<'button'>

export const InlineCitationCarouselNext = ({
  className,
  ...props
}: InlineCitationCarouselNextProps) => {
  const { t } = useTranslation()
  const api = useCarouselApi()

  const handleClick = useCallback(() => {
    if (api) {
      api.scrollNext()
    }
  }, [api])

  return (
    <Button
      variant='ghost'
      size='icon-sm'
      aria-label={t('Next')}
      className={cn('shrink-0', className)}
      onClick={handleClick}
      type='button'
      {...props}
    >
      <ArrowRightIcon className='text-muted-foreground size-4' />
    </Button>
  )
}

export type InlineCitationSourceProps = ComponentProps<'div'> & {
  title?: string
  url?: string
  description?: string
}

export const InlineCitationSource = ({
  title,
  url,
  description,
  className,
  children,
  ...props
}: InlineCitationSourceProps) => (
  <div className={cn('space-y-1', className)} {...props}>
    {title && (
      <h4 className='truncate text-sm leading-tight font-medium'>{title}</h4>
    )}
    {url && (
      <p className='text-muted-foreground truncate text-xs break-all'>{url}</p>
    )}
    {description && (
      <p className='text-muted-foreground line-clamp-3 text-sm leading-relaxed'>
        {description}
      </p>
    )}
    {children}
  </div>
)

export type InlineCitationQuoteProps = ComponentProps<'blockquote'>

export const InlineCitationQuote = ({
  children,
  className,
  ...props
}: InlineCitationQuoteProps) => (
  <blockquote
    className={cn(
      'border-muted text-muted-foreground border-l-2 pl-3 text-sm italic',
      className
    )}
    {...props}
  >
    {children}
  </blockquote>
)
