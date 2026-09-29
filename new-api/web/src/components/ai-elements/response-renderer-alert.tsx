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
import { t } from 'i18next'
import type { ReactNode } from 'react'
import type { BlockquoteNode, ParsedNode } from 'stream-markdown-parser'

import { cn } from '@/lib/utils'

import { hasParsedChildren } from './response-node-guards'
import type {
  AlertConfig,
  AlertKind,
  BlockRendererOptions,
} from './response-types'

const alertConfig = {
  note: {
    label: 'Note',
    className:
      'border-blue-500/40 bg-blue-500/8 text-blue-950 dark:text-blue-100',
    markerClassName: 'text-blue-600 dark:text-blue-300',
  },
  tip: {
    label: 'Tip',
    className:
      'border-emerald-500/40 bg-emerald-500/8 text-emerald-950 dark:text-emerald-100',
    markerClassName: 'text-emerald-600 dark:text-emerald-300',
  },
  important: {
    label: 'Important',
    className:
      'border-violet-500/40 bg-violet-500/8 text-violet-950 dark:text-violet-100',
    markerClassName: 'text-violet-600 dark:text-violet-300',
  },
  warning: {
    label: 'Warning',
    className:
      'border-amber-500/40 bg-amber-500/8 text-amber-950 dark:text-amber-100',
    markerClassName: 'text-amber-600 dark:text-amber-300',
  },
  caution: {
    label: 'Caution',
    className: 'border-red-500/40 bg-red-500/8 text-red-950 dark:text-red-100',
    markerClassName: 'text-red-600 dark:text-red-300',
  },
} satisfies Record<AlertKind, AlertConfig>

function getAlertKind(node: BlockquoteNode): AlertKind | null {
  const firstChild = node.children[0]
  if (!firstChild || firstChild.type !== 'paragraph') {
    return null
  }

  if (!hasParsedChildren(firstChild)) {
    return null
  }

  const firstInline = firstChild.children[0]
  if (!firstInline || firstInline.type !== 'text') {
    return null
  }

  if (!('content' in firstInline) || typeof firstInline.content !== 'string') {
    return null
  }

  const markerPattern = /^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]\s*\n?/i
  const match = firstInline.content.match(markerPattern)
  if (!match) {
    return null
  }

  return match[1].toLowerCase() as AlertKind
}

function getAlertChildren(node: BlockquoteNode, kind: AlertKind): ParsedNode[] {
  const firstChild = node.children[0]
  if (!firstChild || firstChild.type !== 'paragraph') {
    return node.children
  }

  if (!hasParsedChildren(firstChild)) {
    return node.children
  }

  const firstInline = firstChild.children[0]
  if (!firstInline || firstInline.type !== 'text') {
    return node.children
  }

  if (!('content' in firstInline) || typeof firstInline.content !== 'string') {
    return node.children
  }

  const marker = `[!${kind.toUpperCase()}]`
  const content = firstInline.content.replace(marker, '').replace(/^\s*\n?/, '')
  const nextParagraph = {
    ...firstChild,
    children: [
      { ...firstInline, content, raw: content },
      ...firstChild.children.slice(1),
    ],
  }

  if (!content && nextParagraph.children.length === 1) {
    return node.children.slice(1)
  }

  return [nextParagraph, ...node.children.slice(1)]
}

export function renderBlockquote(
  node: BlockquoteNode,
  key: string,
  options: BlockRendererOptions
): ReactNode {
  const alertKind = getAlertKind(node)
  if (alertKind) {
    const config = alertConfig[alertKind]
    const alertChildren = getAlertChildren(node, alertKind)

    return (
      <aside
        className={cn(
          'my-4 rounded-lg border px-4 py-3 text-sm',
          '[&>*:first-child]:mt-0 [&>*:last-child]:mb-0',
          config.className
        )}
        key={key}
      >
        <div
          className={cn(
            'mb-2 text-xs font-semibold tracking-wide uppercase',
            config.markerClassName
          )}
        >
          {t(config.label)}
        </div>
        {options.renderChildren(alertChildren)}
      </aside>
    )
  }

  return (
    <blockquote
      className='border-border text-muted-foreground my-4 border-l-2 pl-4'
      key={key}
    >
      {options.renderChildren(node.children)}
    </blockquote>
  )
}
