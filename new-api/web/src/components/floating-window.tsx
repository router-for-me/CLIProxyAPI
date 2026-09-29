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
import { ArrowExpand02Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { ChevronsUpDown, GripHorizontal, X } from 'lucide-react'
import {
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export type FloatingWindowPosition = {
  x: number
  y: number
}

type FloatingWindowSize = {
  width: number
  /** Unset until the user resizes vertically; content decides until then. */
  height?: number
}

type FloatingWindowProps = {
  title: ReactNode
  children: ReactNode
  onClose: () => void
  /** Viewport coordinates used until the user moves the window. */
  defaultPosition: FloatingWindowPosition
  defaultWidth: number
  defaultHeight?: number
  minWidth?: number
  minHeight?: number
  /** Small element rendered beside the title, e.g. a count badge. */
  badge?: ReactNode
  footer?: ReactNode
  /**
   * Persists position, size and collapsed state in `localStorage` under this
   * key so the window reopens where and how the user left it.
   */
  storageKey?: string
  /** A new request expands the window without resetting its position or size. */
  expandRequest?: number
  className?: string
}

type FloatingWindowState = {
  position: FloatingWindowPosition
  size: FloatingWindowSize
  collapsed: boolean
}

type ResizeDirection = 'n' | 's' | 'e' | 'w' | 'ne' | 'nw' | 'se' | 'sw'

const RESIZE_DIRECTIONS: ResizeDirection[] = [
  'n',
  's',
  'e',
  'w',
  'ne',
  'nw',
  'se',
  'sw',
]
const RESIZE_HANDLE_CLASS: Record<ResizeDirection, string> = {
  n: 'top-0 left-2 right-2 h-1.5 cursor-ns-resize',
  s: 'bottom-0 left-2 right-2 h-1.5 cursor-ns-resize',
  e: 'top-2 bottom-2 right-0 w-1.5 cursor-ew-resize',
  w: 'top-2 bottom-2 left-0 w-1.5 cursor-ew-resize',
  ne: 'top-0 right-0 size-3 cursor-nesw-resize',
  nw: 'top-0 left-0 size-3 cursor-nwse-resize',
  se: 'bottom-0 right-0 size-8 cursor-nwse-resize',
  sw: 'bottom-0 left-0 size-3 cursor-nesw-resize',
}

const VIEWPORT_MARGIN = 8
/** Keeps at least the header reachable when the window is dragged downward. */
const HEADER_HEIGHT = 48
const DEFAULT_MIN_WIDTH = 280
const DEFAULT_MIN_HEIGHT = 200

function storageKeyFor(key: string) {
  return `floating-window:${key}`
}

function readStoredState(key?: string): FloatingWindowState | null {
  if (!key) return null
  try {
    const raw = window.localStorage.getItem(storageKeyFor(key))
    if (!raw) return null
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object') return null
    const stored = parsed as Record<string, unknown>
    if (typeof stored.x !== 'number' || typeof stored.y !== 'number') {
      return null
    }
    return {
      position: { x: stored.x, y: stored.y },
      size: {
        width: typeof stored.width === 'number' ? stored.width : Number.NaN,
        height: typeof stored.height === 'number' ? stored.height : undefined,
      },
      collapsed: stored.collapsed === true,
    }
  } catch {
    return null
  }
}

function clampPosition(
  position: FloatingWindowPosition,
  width: number
): FloatingWindowPosition {
  const maxX = Math.max(
    VIEWPORT_MARGIN,
    window.innerWidth - width - VIEWPORT_MARGIN
  )
  const maxY = Math.max(
    VIEWPORT_MARGIN,
    window.innerHeight - HEADER_HEIGHT - VIEWPORT_MARGIN
  )
  return {
    x: Math.min(Math.max(VIEWPORT_MARGIN, position.x), maxX),
    y: Math.min(Math.max(VIEWPORT_MARGIN, position.y), maxY),
  }
}

function clampSize(
  size: FloatingWindowSize,
  limits: { minWidth: number; minHeight: number }
): FloatingWindowSize {
  const maxWidth = Math.max(
    limits.minWidth,
    window.innerWidth - VIEWPORT_MARGIN * 2
  )
  const maxHeight = Math.max(
    limits.minHeight,
    window.innerHeight - VIEWPORT_MARGIN * 2
  )
  return {
    width: Math.min(Math.max(limits.minWidth, size.width), maxWidth),
    height:
      size.height === undefined
        ? undefined
        : Math.min(Math.max(limits.minHeight, size.height), maxHeight),
  }
}

/**
 * Non-modal window that floats above the page: dragged by its header, resized
 * from any edge or corner, collapsible to a pill. It renders where it is
 * mounted (no portal) with `position: fixed`, so a parent modal keeps
 * treating it as its own content.
 */
export function FloatingWindow(props: FloatingWindowProps) {
  const { t } = useTranslation()
  const id = useId()
  const limits = useMemo(
    () => ({
      minWidth: props.minWidth ?? DEFAULT_MIN_WIDTH,
      minHeight: props.minHeight ?? DEFAULT_MIN_HEIGHT,
    }),
    [props.minWidth, props.minHeight]
  )
  const [state, setState] = useState<FloatingWindowState>(() => {
    const stored = readStoredState(props.storageKey)
    const size = clampSize(
      {
        width:
          stored && !Number.isNaN(stored.size.width)
            ? stored.size.width
            : props.defaultWidth,
        height: stored ? stored.size.height : props.defaultHeight,
      },
      limits
    )
    return {
      position: clampPosition(
        stored?.position ?? props.defaultPosition,
        size.width
      ),
      size,
      collapsed:
        props.expandRequest === undefined && (stored?.collapsed ?? false),
    }
  })
  const windowRef = useRef<HTMLElement>(null)
  const dragRef = useRef<{
    pointerId: number
    offsetX: number
    offsetY: number
  } | null>(null)
  const resizeRef = useRef<{
    pointerId: number
    direction: ResizeDirection
    startX: number
    startY: number
    position: FloatingWindowPosition
    size: { width: number; height: number }
  } | null>(null)

  useEffect(() => {
    if (props.expandRequest === undefined) return
    setState((previous) =>
      previous.collapsed ? { ...previous, collapsed: false } : previous
    )
  }, [props.expandRequest])

  useEffect(() => {
    if (!props.storageKey) return
    try {
      window.localStorage.setItem(
        storageKeyFor(props.storageKey),
        JSON.stringify({
          x: state.position.x,
          y: state.position.y,
          width: state.size.width,
          height: state.size.height,
          collapsed: state.collapsed,
        })
      )
    } catch {
      // Storage may be unavailable; the window still works for this session.
    }
  }, [props.storageKey, state])

  useEffect(() => {
    const keepInViewport = () =>
      setState((previous) => {
        const size = clampSize(previous.size, limits)
        return {
          ...previous,
          size,
          position: clampPosition(previous.position, size.width),
        }
      })
    window.addEventListener('resize', keepInViewport)
    return () => window.removeEventListener('resize', keepInViewport)
  }, [limits])

  const capturePointer = (
    event: ReactPointerEvent<HTMLElement>,
    capture: boolean
  ) => {
    const method = capture ? 'setPointerCapture' : 'releasePointerCapture'
    if (typeof event.currentTarget[method] === 'function') {
      event.currentTarget[method](event.pointerId)
    }
  }

  const handleDragStart = (event: ReactPointerEvent<HTMLElement>) => {
    if (event.button !== 0) return
    if (
      event.target instanceof Element &&
      event.target.closest('button, a, input, select, textarea')
    ) {
      return
    }
    dragRef.current = {
      pointerId: event.pointerId,
      offsetX: event.clientX - state.position.x,
      offsetY: event.clientY - state.position.y,
    }
    capturePointer(event, true)
  }

  const handleDragMove = (event: ReactPointerEvent<HTMLElement>) => {
    const drag = dragRef.current
    if (!drag || drag.pointerId !== event.pointerId) return
    const next = clampPosition(
      { x: event.clientX - drag.offsetX, y: event.clientY - drag.offsetY },
      state.size.width
    )
    setState((previous) => ({ ...previous, position: next }))
  }

  const handleDragEnd = (event: ReactPointerEvent<HTMLElement>) => {
    if (dragRef.current?.pointerId !== event.pointerId) return
    dragRef.current = null
    capturePointer(event, false)
  }

  const handleResizeStart = (
    event: ReactPointerEvent<HTMLElement>,
    direction: ResizeDirection
  ) => {
    if (event.button !== 0) return
    event.preventDefault()
    // Until the user has set a height the window is content-sized, so the
    // rendered height becomes the starting point of a vertical resize.
    const renderedHeight = windowRef.current?.getBoundingClientRect().height
    resizeRef.current = {
      pointerId: event.pointerId,
      direction,
      startX: event.clientX,
      startY: event.clientY,
      position: state.position,
      size: {
        width: state.size.width,
        height:
          state.size.height ?? Math.max(renderedHeight ?? 0, limits.minHeight),
      },
    }
    capturePointer(event, true)
  }

  const handleResizeMove = (event: ReactPointerEvent<HTMLElement>) => {
    const resize = resizeRef.current
    if (!resize || resize.pointerId !== event.pointerId) return
    const dx = event.clientX - resize.startX
    const dy = event.clientY - resize.startY
    let { width, height } = resize.size
    let { x, y } = resize.position
    if (resize.direction.includes('e')) width = resize.size.width + dx
    if (resize.direction.includes('s')) height = resize.size.height + dy
    if (resize.direction.includes('w')) width = resize.size.width - dx
    if (resize.direction.includes('n')) height = resize.size.height - dy
    const size = clampSize({ width, height }, limits)
    if (resize.direction.includes('w')) {
      x = resize.position.x + (resize.size.width - size.width)
    }
    if (resize.direction.includes('n')) {
      y = resize.position.y + (resize.size.height - (size.height ?? height))
    }
    const position = clampPosition({ x, y }, size.width)
    setState((previous) => ({ ...previous, size, position }))
  }

  const handleResizeEnd = (event: ReactPointerEvent<HTMLElement>) => {
    if (resizeRef.current?.pointerId !== event.pointerId) return
    resizeRef.current = null
    capturePointer(event, false)
  }

  const dragHandleProps = {
    title: t('Drag to move'),
    onPointerDown: handleDragStart,
    onPointerMove: handleDragMove,
    onPointerUp: handleDragEnd,
    onPointerCancel: handleDragEnd,
  }
  const style = {
    position: 'fixed' as const,
    left: state.position.x,
    top: state.position.y,
  }

  if (state.collapsed) {
    return (
      <div
        {...dragHandleProps}
        style={style}
        className={cn(
          'bg-background border-border z-50 flex cursor-move touch-none items-center gap-2 rounded-full border py-1 pr-1 pl-3 shadow-lg select-none',
          props.className
        )}
      >
        <GripHorizontal
          className='text-muted-foreground size-3.5 shrink-0'
          aria-hidden='true'
        />
        <span className='text-sm font-semibold'>{props.title}</span>
        {props.badge}
        <Button
          type='button'
          variant='ghost'
          size='icon-xs'
          aria-label={t('Expand panel')}
          onClick={() =>
            setState((previous) => ({ ...previous, collapsed: false }))
          }
        >
          <ChevronsUpDown aria-hidden='true' />
        </Button>
        <Button
          type='button'
          variant='ghost'
          size='icon-xs'
          aria-label={t('Close panel')}
          onClick={props.onClose}
        >
          <X aria-hidden='true' />
        </Button>
      </div>
    )
  }

  return (
    <aside
      ref={windowRef}
      role='complementary'
      aria-labelledby={`${id}-title`}
      style={{
        ...style,
        width: state.size.width,
        height: state.size.height,
        maxHeight: `calc(100vh - ${VIEWPORT_MARGIN * 2}px)`,
      }}
      className={cn(
        'bg-background border-border z-50 flex flex-col overflow-hidden rounded-xl border shadow-xl',
        props.className
      )}
    >
      <header
        {...dragHandleProps}
        className='border-border/70 flex cursor-move touch-none items-center gap-2 border-b px-4 py-3 select-none'
      >
        <GripHorizontal
          className='text-muted-foreground size-3.5 shrink-0'
          aria-hidden='true'
        />
        <h3 id={`${id}-title`} className='text-sm font-semibold'>
          {props.title}
        </h3>
        {props.badge}
        <div className='ml-auto flex items-center gap-1'>
          <Button
            type='button'
            variant='ghost'
            size='icon-xs'
            aria-label={t('Collapse panel')}
            onClick={() =>
              setState((previous) => ({ ...previous, collapsed: true }))
            }
          >
            <ChevronsUpDown aria-hidden='true' />
          </Button>
          <Button
            type='button'
            variant='ghost'
            size='icon-xs'
            aria-label={t('Close panel')}
            onClick={props.onClose}
          >
            <X aria-hidden='true' />
          </Button>
        </div>
      </header>
      <div className='min-h-0 flex-1 overflow-y-auto px-4 py-3'>
        {props.children}
      </div>
      {props.footer && (
        <footer className='border-border/70 border-t px-4 py-3'>
          {props.footer}
        </footer>
      )}
      <div className='border-border/50 bg-muted/30 text-muted-foreground flex h-8 shrink-0 items-center justify-end border-t pr-9 pl-4 text-xs select-none'>
        {t('Drag to resize')}
      </div>
      {RESIZE_DIRECTIONS.map((direction) => (
        <div
          key={direction}
          data-slot='floating-window-resize'
          data-direction={direction}
          title={t('Drag to resize')}
          aria-hidden='true'
          className={cn(
            'absolute z-10 touch-none',
            RESIZE_HANDLE_CLASS[direction],
            direction === 'se' &&
              'text-muted-foreground hover:bg-accent hover:text-accent-foreground flex items-center justify-center rounded-tl-md'
          )}
          onPointerDown={(event) => handleResizeStart(event, direction)}
          onPointerMove={handleResizeMove}
          onPointerUp={handleResizeEnd}
          onPointerCancel={handleResizeEnd}
        >
          {direction === 'se' && (
            <HugeiconsIcon
              icon={ArrowExpand02Icon}
              className='pointer-events-none size-4'
              aria-hidden='true'
            />
          )}
        </div>
      ))}
    </aside>
  )
}
