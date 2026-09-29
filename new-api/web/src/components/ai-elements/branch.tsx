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

import type { UIMessage } from 'ai'
import { ChevronLeftIcon, ChevronRightIcon } from 'lucide-react'
import {
  type ComponentProps,
  createContext,
  type HTMLAttributes,
  type ReactElement,
  useContext,
  useEffect,
  useMemo,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

type BranchContextType = {
  currentBranch: number
  totalBranches: number
  goToPrevious: () => void
  goToNext: () => void
  branches: ReactElement[]
  setBranches: (branches: ReactElement[]) => void
}

const BranchContext = createContext<BranchContextType | null>(null)

const useBranch = () => {
  const context = useContext(BranchContext)

  if (!context) {
    throw new Error('Branch components must be used within Branch')
  }

  return context
}

export type BranchProps = HTMLAttributes<HTMLDivElement> & {
  defaultBranch?: number
  onBranchChange?: (branchIndex: number) => void
}

export const Branch = ({
  defaultBranch = 0,
  onBranchChange,
  className,
  ...props
}: BranchProps) => {
  const [currentBranch, setCurrentBranch] = useState(defaultBranch)
  const [branches, setBranches] = useState<ReactElement[]>([])

  const handleBranchChange = (newBranch: number) => {
    setCurrentBranch(newBranch)
    onBranchChange?.(newBranch)
  }

  const goToPrevious = () => {
    const newBranch =
      currentBranch > 0 ? currentBranch - 1 : branches.length - 1
    handleBranchChange(newBranch)
  }

  const goToNext = () => {
    const newBranch =
      currentBranch < branches.length - 1 ? currentBranch + 1 : 0
    handleBranchChange(newBranch)
  }

  const contextValue: BranchContextType = {
    currentBranch,
    totalBranches: branches.length,
    goToPrevious,
    goToNext,
    branches,
    setBranches,
  }

  return (
    <BranchContext.Provider value={contextValue}>
      <div
        className={cn('grid w-full gap-2 [&>div]:pb-0', className)}
        {...props}
      />
    </BranchContext.Provider>
  )
}

export type BranchMessagesProps = HTMLAttributes<HTMLDivElement>

export const BranchMessages = ({ children, ...props }: BranchMessagesProps) => {
  const { currentBranch, setBranches, branches } = useBranch()
  const childrenArray = useMemo(
    () => (Array.isArray(children) ? children : [children]),
    [children]
  )

  // Use useEffect to update branches when they change
  useEffect(() => {
    if (branches.length !== childrenArray.length) {
      setBranches(childrenArray)
    }
  }, [childrenArray, branches, setBranches])

  return childrenArray.map((branch, index) => (
    <div
      className={cn(
        'grid gap-2 overflow-hidden [&>div]:pb-0',
        index === currentBranch ? 'block' : 'hidden'
      )}
      key={branch.key}
      {...props}
    >
      {branch}
    </div>
  ))
}

export type BranchSelectorProps = HTMLAttributes<HTMLDivElement> & {
  from: UIMessage['role']
}

export const BranchSelector = ({
  className,
  from,
  ...props
}: BranchSelectorProps) => {
  const { totalBranches } = useBranch()

  // Don't render if there's only one branch
  if (totalBranches <= 1) {
    return null
  }

  return (
    <div
      className={cn(
        'flex items-center gap-2 self-end px-10',
        from === 'assistant' ? 'justify-start' : 'justify-end',
        className
      )}
      {...props}
    />
  )
}

export type BranchPreviousProps = ComponentProps<typeof Button>

export const BranchPrevious = ({
  className,
  children,
  ...props
}: BranchPreviousProps) => {
  const { t } = useTranslation()
  const { goToPrevious, totalBranches } = useBranch()

  return (
    <Button
      aria-label={t('Previous branch')}
      className={cn(
        'text-muted-foreground size-7 shrink-0 transition-colors',
        'hover:bg-accent hover:text-foreground',
        'disabled:pointer-events-none disabled:opacity-50',
        className
      )}
      disabled={totalBranches <= 1}
      onClick={goToPrevious}
      size='icon'
      type='button'
      variant='ghost'
      {...props}
    >
      {children ?? <ChevronLeftIcon size={14} />}
    </Button>
  )
}

export type BranchNextProps = ComponentProps<typeof Button>

export const BranchNext = ({
  className,
  children,
  ...props
}: BranchNextProps) => {
  const { t } = useTranslation()
  const { goToNext, totalBranches } = useBranch()

  return (
    <Button
      aria-label={t('Next branch')}
      className={cn(
        'text-muted-foreground size-7 shrink-0 transition-colors',
        'hover:bg-accent hover:text-foreground',
        'disabled:pointer-events-none disabled:opacity-50',
        className
      )}
      disabled={totalBranches <= 1}
      onClick={goToNext}
      size='icon'
      type='button'
      variant='ghost'
      {...props}
    >
      {children ?? <ChevronRightIcon size={14} />}
    </Button>
  )
}

export type BranchPageProps = HTMLAttributes<HTMLSpanElement>

export const BranchPage = ({ className, ...props }: BranchPageProps) => {
  const { t } = useTranslation()
  const { currentBranch, totalBranches } = useBranch()

  return (
    <span
      className={cn(
        'text-muted-foreground text-xs font-medium tabular-nums',
        className
      )}
      {...props}
    >
      {currentBranch + 1} {t('of')} {totalBranches}
    </span>
  )
}
