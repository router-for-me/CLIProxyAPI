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
import type { ReactNode } from 'react'
import type { FootnoteNode, ParsedNode } from 'stream-markdown-parser'

import { getNodeKey } from './response-content'
import type { FadeRun } from './response-fade'
import {
  hasParsedChildren,
  isBlockquoteNode,
  isCodeBlockNode,
  isDefinitionListNode,
  isHeadingNode,
  isHtmlBlockNode,
  isImageNode,
  isLinkNode,
  isListNode,
  isMathBlockNode,
  isMathInlineNode,
  isTableNode,
  isTextNode,
} from './response-node-guards'
import { renderBlockquote } from './response-renderer-alert'
import {
  renderCodeBlock,
  renderDefinitionList,
  renderHeading,
  renderList,
  renderMathBlock,
  renderMathInline,
} from './response-renderer-blocks'
import { renderDetails } from './response-renderer-details'
import { renderFootnotes as renderFootnotesBlock } from './response-renderer-footnotes'
import {
  renderImage,
  renderLink,
  renderTextNode,
} from './response-renderer-inline'
import { renderTable } from './response-renderer-table'
import type { BlockRendererOptions, RenderChildren } from './response-types'

function createRenderChildren(fadeRun?: FadeRun): RenderChildren {
  return (nodes) => renderChildren(nodes, fadeRun)
}

export function renderChildren(
  nodes: ParsedNode[],
  fadeRun?: FadeRun
): ReactNode {
  const options: BlockRendererOptions = {
    fadeRun,
    renderChildren: createRenderChildren(fadeRun),
  }
  return nodes.map((node, index) =>
    renderNode(node, getNodeKey(node, index), options)
  )
}

export function renderFootnotes(
  footnotes: FootnoteNode[],
  fadeRun?: FadeRun
): ReactNode {
  return renderFootnotesBlock(footnotes, {
    fadeRun,
    renderChildren: createRenderChildren(fadeRun),
  })
}

/** Settled (non-animated) renderChildren for skipped subtrees */
const settledRenderChildren = createRenderChildren()

function renderNode(
  node: ParsedNode,
  key: string,
  options: BlockRendererOptions
): ReactNode {
  if (isTextNode(node)) {
    return renderTextNode(node, options.fadeRun)
  }

  if (isHeadingNode(node)) {
    return renderHeading(node, key, options)
  }

  if (node.type === 'paragraph' && hasParsedChildren(node)) {
    return (
      <p className='my-3 leading-7' key={key}>
        {options.renderChildren(node.children)}
      </p>
    )
  }

  if (node.type === 'inline' && hasParsedChildren(node)) {
    return <span key={key}>{options.renderChildren(node.children)}</span>
  }

  if (isListNode(node)) {
    return renderList(node, key, options)
  }

  // Skip list: code / math / html / image — no fade wrapping, offset not advanced
  if (isCodeBlockNode(node)) {
    return renderCodeBlock(node, key)
  }

  if (node.type === 'inline_code' && 'code' in node) {
    return (
      <code
        className='bg-muted/70 text-foreground rounded px-1 py-0.5 font-mono text-[0.9em]'
        key={key}
      >
        {String(node.code)}
      </code>
    )
  }

  if (isLinkNode(node)) {
    return renderLink(node, key, options.renderChildren)
  }

  if (isImageNode(node)) {
    return renderImage(node, key)
  }

  if (isBlockquoteNode(node)) {
    return renderBlockquote(node, key, options)
  }

  if (isTableNode(node)) {
    return renderTable(node, key, options)
  }

  if (isDefinitionListNode(node)) {
    return renderDefinitionList(node, key, options)
  }

  if (node.type === 'strong' && hasParsedChildren(node)) {
    return (
      <strong className='text-foreground font-semibold' key={key}>
        {options.renderChildren(node.children)}
      </strong>
    )
  }

  if (node.type === 'emphasis' && hasParsedChildren(node)) {
    return <em key={key}>{options.renderChildren(node.children)}</em>
  }

  if (node.type === 'strikethrough' && hasParsedChildren(node)) {
    return <del key={key}>{options.renderChildren(node.children)}</del>
  }

  if (node.type === 'highlight' && hasParsedChildren(node)) {
    return <mark key={key}>{options.renderChildren(node.children)}</mark>
  }

  if (node.type === 'insert' && hasParsedChildren(node)) {
    return <ins key={key}>{options.renderChildren(node.children)}</ins>
  }

  if (node.type === 'subscript' && hasParsedChildren(node)) {
    return <sub key={key}>{options.renderChildren(node.children)}</sub>
  }

  if (node.type === 'superscript' && hasParsedChildren(node)) {
    return <sup key={key}>{options.renderChildren(node.children)}</sup>
  }

  if (
    (node.type === 'checkbox' || node.type === 'checkbox_input') &&
    'checked' in node
  ) {
    return (
      <input
        checked={Boolean(node.checked)}
        className='accent-primary mr-2 size-4 align-[-0.15em]'
        disabled
        key={key}
        readOnly
        type='checkbox'
      />
    )
  }

  if (node.type === 'hardbreak') {
    return <br key={key} />
  }

  if (node.type === 'thematic_break') {
    return <hr className='border-border/70 my-6' key={key} />
  }

  if (isMathBlockNode(node)) {
    return renderMathBlock(node, key)
  }

  if (isMathInlineNode(node)) {
    return renderMathInline(node, key)
  }

  if (node.type === 'footnote_reference' && 'id' in node) {
    return (
      <sup className='text-primary mx-0.5 text-xs' key={key}>
        <a
          className='underline-offset-2 hover:underline'
          href={`#footnote-${String(node.id)}`}
          id={`footnote-ref-${String(node.id)}`}
        >
          [{String(node.id)}]
        </a>
      </sup>
    )
  }

  if (node.type === 'footnote_anchor') {
    return null
  }

  if (isHtmlBlockNode(node) && node.tag === 'details') {
    return renderDetails(node, key, {
      renderChildren: settledRenderChildren,
    })
  }

  if (node.type === 'html_block' && 'content' in node) {
    return <span key={key}>{String(node.content)}</span>
  }

  if (node.type === 'html_inline' && 'content' in node) {
    return <span key={key}>{String(node.content)}</span>
  }

  if (hasParsedChildren(node)) {
    return <span key={key}>{options.renderChildren(node.children)}</span>
  }

  if ('content' in node && typeof node.content === 'string') {
    return <span key={key}>{node.content}</span>
  }

  return <span key={key}>{node.raw}</span>
}
