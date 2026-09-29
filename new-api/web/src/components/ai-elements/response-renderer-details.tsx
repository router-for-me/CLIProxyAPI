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
import type { HtmlBlockNode, ParsedNode } from 'stream-markdown-parser'

import { hasParsedChildren, isHtmlBlockNode } from './response-node-guards'
import type { BlockRendererOptions } from './response-types'

export function renderDetails(
  node: HtmlBlockNode,
  key: string,
  options: BlockRendererOptions
): ReactNode {
  const children = Array.isArray(node.children) ? node.children : []
  const summaryNode = children.find(isSummaryHtmlNode)
  const contentNodes = children.filter((child) => child !== summaryNode)
  const summary = getDetailsSummary(summaryNode, options)

  return (
    <details
      className='border-border/70 my-4 rounded-lg border px-4 py-3'
      key={key}
    >
      <summary className='text-foreground cursor-pointer text-sm font-semibold'>
        {summary}
      </summary>
      <div className='border-border/70 mt-3 border-l pl-4 [&>*:first-child]:mt-0 [&>*:last-child]:mb-0'>
        {options.renderChildren(contentNodes)}
      </div>
    </details>
  )
}

function isSummaryHtmlNode(node: ParsedNode): node is HtmlBlockNode {
  return isHtmlBlockNode(node) && node.tag === 'summary'
}

function getDetailsSummary(
  node: HtmlBlockNode | undefined,
  options: BlockRendererOptions
): ReactNode {
  if (!node || !hasParsedChildren(node)) {
    return t('Details')
  }

  return options.renderChildren(node.children)
}
