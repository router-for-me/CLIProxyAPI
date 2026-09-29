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
import { Monitor, Sun, MoonStar } from 'lucide-react'
import { motion } from 'motion/react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { useTheme } from '@/context/theme-provider'
import { cn } from '@/lib/utils'

export function ThemeQuickSwitcher() {
  const { t } = useTranslation()
  const { theme, setTheme } = useTheme()

  return (
    <div className='px-2 pt-1.5 pb-1'>
      <div className='flex w-full items-center justify-between gap-3'>
        <span
          id='theme-switcher-label'
          className='text-muted-foreground text-sm select-none'
        >
          {t('Theme')}
        </span>
        <div
          role='radiogroup'
          aria-labelledby='theme-switcher-label'
          className='border-muted/50 bg-muted/40 inline-flex w-auto items-center gap-1.5 rounded-lg border px-1.5 py-1'
        >
          <Button
            variant='ghost'
            size='icon'
            role='radio'
            aria-label={t('System')}
            aria-checked={theme === 'system'}
            onClick={() => setTheme('system')}
            className={cn(
              'relative size-7',
              theme === 'system' && 'text-accent-foreground'
            )}
          >
            {theme === 'system' && (
              <motion.span
                layoutId='theme-switcher-active'
                className='bg-accent ring-border absolute inset-0 rounded-md ring-1'
                transition={{
                  type: 'spring',
                  stiffness: 500,
                  damping: 30,
                  mass: 0.2,
                }}
                animate={{ rotate: 360 }}
              />
            )}
            <Monitor className='relative z-10 size-[0.95rem]' />
          </Button>
          <Button
            variant='ghost'
            size='icon'
            role='radio'
            aria-label={t('Light')}
            aria-checked={theme === 'light'}
            onClick={() => setTheme('light')}
            className={cn(
              'relative size-7',
              theme === 'light' && 'text-accent-foreground'
            )}
          >
            {theme === 'light' && (
              <motion.span
                layoutId='theme-switcher-active'
                className='bg-accent ring-border absolute inset-0 rounded-md ring-1'
                transition={{
                  type: 'spring',
                  stiffness: 500,
                  damping: 30,
                  mass: 0.2,
                }}
                animate={{ rotate: 360 }}
              />
            )}
            <Sun className='relative z-10 size-[0.95rem]' />
          </Button>
          <Button
            variant='ghost'
            size='icon'
            role='radio'
            aria-label={t('Dark')}
            aria-checked={theme === 'dark'}
            onClick={() => setTheme('dark')}
            className={cn(
              'relative size-7',
              theme === 'dark' && 'text-accent-foreground'
            )}
          >
            {theme === 'dark' && (
              <motion.span
                layoutId='theme-switcher-active'
                className='bg-accent ring-border absolute inset-0 rounded-md ring-1'
                transition={{
                  type: 'spring',
                  stiffness: 500,
                  damping: 30,
                  mass: 0.2,
                }}
                animate={{ rotate: 360 }}
              />
            )}
            <MoonStar className='relative z-10 size-[0.95rem]' />
          </Button>
        </div>
      </div>
    </div>
  )
}
