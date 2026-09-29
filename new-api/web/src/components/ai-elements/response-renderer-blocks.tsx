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
import type {
  CodeBlockNode,
  DefinitionItemNode,
  DefinitionListNode,
  HeadingNode,
  ListNode,
  MathBlockNode,
  MathInlineNode,
} from 'stream-markdown-parser'

import {
  CodeBlock,
  CodeBlockCopyButton,
} from '@/components/ai-elements/code-block'
import { cn } from '@/lib/utils'

import { getNodeKey } from './response-content'
import type { BlockRendererOptions } from './response-types'

const headingClasses = {
  1: 'mt-6 mb-3 text-xl font-semibold tracking-normal',
  2: 'mt-6 mb-3 text-lg font-semibold tracking-normal',
  3: 'mt-5 mb-2 text-base font-semibold tracking-normal',
  4: 'mt-5 mb-2 text-sm font-semibold tracking-normal',
  5: 'text-muted-foreground mt-4 mb-2 text-sm font-semibold tracking-normal',
  6: 'text-muted-foreground mt-4 mb-2 text-xs font-semibold tracking-normal uppercase',
} satisfies Record<1 | 2 | 3 | 4 | 5 | 6, string>

export function renderHeading(
  node: HeadingNode,
  key: string,
  options: BlockRendererOptions
): ReactNode {
  const headingLevel = Math.min(Math.max(node.level, 1), 6) as
    | 1
    | 2
    | 3
    | 4
    | 5
    | 6
  const className = headingClasses[headingLevel]
  const children = options.renderChildren(node.children)

  if (headingLevel === 1) {
    return (
      <h1 className={className} key={key}>
        {children}
      </h1>
    )
  }

  if (headingLevel === 2) {
    return (
      <h2 className={className} key={key}>
        {children}
      </h2>
    )
  }

  if (headingLevel === 3) {
    return (
      <h3 className={className} key={key}>
        {children}
      </h3>
    )
  }

  if (headingLevel === 4) {
    return (
      <h4 className={className} key={key}>
        {children}
      </h4>
    )
  }

  if (headingLevel === 5) {
    return (
      <h5 className={className} key={key}>
        {children}
      </h5>
    )
  }

  return (
    <h6 className={className} key={key}>
      {children}
    </h6>
  )
}

export function renderList(
  node: ListNode,
  key: string,
  options: BlockRendererOptions
): ReactNode {
  const className = cn(
    'my-3 list-outside space-y-1.5 pl-5',
    node.ordered ? 'list-decimal' : 'list-disc'
  )
  const items = node.items.map((item, index) => (
    <li
      className='marker:text-muted-foreground pl-1 leading-7'
      key={getNodeKey(item, index)}
    >
      {options.renderChildren(item.children)}
    </li>
  ))

  if (node.ordered) {
    return (
      <ol className={className} key={key} start={node.start}>
        {items}
      </ol>
    )
  }

  return (
    <ul className={className} key={key}>
      {items}
    </ul>
  )
}

export function renderCodeBlock(node: CodeBlockNode, key: string): ReactNode {
  const language = node.language || 'plaintext'
  const lineCount = node.code.split('\n').length

  return (
    <CodeBlock
      collapsedLines={14}
      code={node.code}
      defaultCollapsed={lineCount > 14}
      key={key}
      language={language}
      maxExpandedLines={44}
      showLineNumbers
      showToolbar
      title={language}
    >
      <CodeBlockCopyButton />
    </CodeBlock>
  )
}

export function renderDefinitionList(
  node: DefinitionListNode,
  key: string,
  options: BlockRendererOptions
): ReactNode {
  return (
    <dl className='my-4 space-y-3' key={key}>
      {node.items.map((item, index) =>
        renderDefinitionItem(item, index, options)
      )}
    </dl>
  )
}

function renderDefinitionItem(
  node: DefinitionItemNode,
  index: number,
  options: BlockRendererOptions
): ReactNode {
  return (
    <div key={`definition-${index}`}>
      <dt className='font-semibold'>{options.renderChildren(node.term)}</dt>
      <dd className='text-muted-foreground mt-1 pl-4'>
        {options.renderChildren(node.definition)}
      </dd>
    </div>
  )
}

export function renderMathBlock(node: MathBlockNode, key: string): ReactNode {
  return (
    <pre
      className='border-border bg-muted/40 my-4 overflow-x-auto rounded-lg border p-4 font-mono text-sm'
      key={key}
    >
      {node.content}
    </pre>
  )
}

export function renderMathInline(node: MathInlineNode, key: string): ReactNode {
  return (
    <code
      className='bg-muted/70 text-foreground rounded px-1 py-0.5 font-mono text-[0.9em]'
      key={key}
    >
      {node.content}
    </code>
  )
}
