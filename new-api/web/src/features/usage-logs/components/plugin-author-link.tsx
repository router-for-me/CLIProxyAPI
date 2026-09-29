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
import { LinkSquare01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'

import { cn } from '@/lib/utils'

import { getSafePluginAuthorUrl } from '../lib/task-artifacts'
import type { TaskPluginAuthor } from '../types'

interface PluginAuthorLinkProps {
  author: TaskPluginAuthor
  showUrl?: boolean
  className?: string
}

export function PluginAuthorLink(props: PluginAuthorLinkProps) {
  const authorUrl = getSafePluginAuthorUrl(props.author)
  const authorName = props.author.name.trim()
  if (!authorName) return null

  if (!authorUrl) {
    return <span className={props.className}>{authorName}</span>
  }

  return (
    <span className={cn('inline-flex min-w-0 flex-col', props.className)}>
      <a
        href={authorUrl}
        target='_blank'
        rel='noopener noreferrer'
        className='inline-flex min-w-0 items-center gap-1 hover:underline'
      >
        <span className='truncate'>{authorName}</span>
        <HugeiconsIcon
          icon={LinkSquare01Icon}
          className='size-3 shrink-0'
          strokeWidth={2}
          aria-hidden='true'
        />
      </a>
      {props.showUrl ? (
        <span className='text-muted-foreground truncate font-mono text-[11px]'>
          {authorUrl}
        </span>
      ) : null}
    </span>
  )
}
