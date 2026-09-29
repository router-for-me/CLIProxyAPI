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
import { Search, X } from 'lucide-react'
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export interface SearchBarProps {
  value: string
  onChange: (value: string) => void
  onClear: () => void
  placeholder?: string
  className?: string
}

export function SearchBar(props: SearchBarProps) {
  const { t } = useTranslation()
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        inputRef.current?.focus()
      }
      if (e.key === 'Escape' && document.activeElement === inputRef.current) {
        inputRef.current?.blur()
      }
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [])

  return (
    <div className={cn('relative', props.className)}>
      <Search className='text-muted-foreground/60 pointer-events-none absolute top-1/2 left-3.5 size-4 -translate-y-1/2' />
      <input
        ref={inputRef}
        type='text'
        placeholder={props.placeholder || t('Search models...')}
        value={props.value}
        onChange={(e) => props.onChange(e.target.value)}
        className={cn(
          'border-border/60 bg-background placeholder:text-muted-foreground/50',
          'hover:border-border',
          'focus:border-primary/50 focus:ring-primary/20 focus:ring-2',
          'h-10 w-full rounded-lg border pr-16 pl-10 text-sm transition-all outline-none'
        )}
        aria-label={t('Search models')}
      />
      <div className='absolute top-1/2 right-2.5 flex -translate-y-1/2 items-center gap-1'>
        {props.value ? (
          <Button
            variant='ghost'
            size='icon'
            onClick={props.onClear}
            className='text-muted-foreground/60 hover:text-foreground size-7'
            aria-label={t('Clear search')}
          >
            <X className='size-4' />
          </Button>
        ) : (
          <kbd className='bg-muted text-muted-foreground pointer-events-none hidden rounded border px-1.5 py-0.5 font-mono text-[10px] sm:inline-block'>
            ⌘K
          </kbd>
        )}
      </div>
    </div>
  )
}
