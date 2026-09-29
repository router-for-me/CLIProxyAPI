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
import type { UseQueryResult } from '@tanstack/react-query'
import { AutoSkeleton } from 'auto-skeleton-react'
import type { ReactNode } from 'react'

import { ErrorState } from '@/components/error-state'
import { useThemeRadiusPx } from '@/lib/theme-radius'

interface ContentSkeletonProps {
  loading: boolean
  children: ReactNode
  borderRadius?: number
  minTextHeight?: number
  maxDepth?: number
  className?: string
}

export function ContentSkeleton(props: ContentSkeletonProps) {
  const themeRadius = useThemeRadiusPx()

  return (
    <div className={props.className}>
      <AutoSkeleton
        loading={props.loading}
        config={{
          animation: 'none',
          baseColor: 'var(--skeleton-base)',
          highlightColor: 'var(--skeleton-highlight)',
          borderRadius: props.borderRadius ?? themeRadius,
          minTextHeight: props.minTextHeight ?? 14,
          maxDepth: props.maxDepth ?? 10,
        }}
      >
        {props.children}
      </AutoSkeleton>
    </div>
  )
}

interface QuerySkeletonProps {
  query: UseQueryResult<unknown, unknown>
  children: ReactNode
  className?: string
  errorTitle?: string
  errorDescription?: string
}

export function QuerySkeleton(props: QuerySkeletonProps) {
  if (props.query.isError) {
    return (
      <ErrorState
        title={props.errorTitle}
        description={props.errorDescription}
        onRetry={() => props.query.refetch()}
      />
    )
  }

  return (
    <ContentSkeleton
      loading={props.query.isLoading}
      className={props.className}
    >
      {props.children}
    </ContentSkeleton>
  )
}
