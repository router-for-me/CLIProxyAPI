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
import type {
  BlockquoteNode,
  CodeBlockNode,
  DefinitionListNode,
  FootnoteNode,
  HeadingNode,
  HtmlBlockNode,
  ImageNode,
  LinkNode,
  ListNode,
  MathBlockNode,
  MathInlineNode,
  ParsedNode,
  TableNode,
  TextNode,
} from 'stream-markdown-parser'

export function hasParsedChildren(
  node: ParsedNode
): node is ParsedNode & { children: ParsedNode[] } {
  return 'children' in node && Array.isArray(node.children)
}

export function isTextNode(node: ParsedNode): node is TextNode {
  return node.type === 'text' && 'content' in node
}

export function isHeadingNode(node: ParsedNode): node is HeadingNode {
  return node.type === 'heading' && 'level' in node && hasParsedChildren(node)
}

export function isListNode(node: ParsedNode): node is ListNode {
  return node.type === 'list' && 'items' in node && Array.isArray(node.items)
}

export function isCodeBlockNode(node: ParsedNode): node is CodeBlockNode {
  return node.type === 'code_block' && 'code' in node && 'language' in node
}

export function isLinkNode(node: ParsedNode): node is LinkNode {
  return node.type === 'link' && 'href' in node && hasParsedChildren(node)
}

export function isImageNode(node: ParsedNode): node is ImageNode {
  return node.type === 'image' && 'src' in node && 'alt' in node
}

export function isBlockquoteNode(node: ParsedNode): node is BlockquoteNode {
  return node.type === 'blockquote' && hasParsedChildren(node)
}

export function isTableNode(node: ParsedNode): node is TableNode {
  return node.type === 'table' && 'header' in node && 'rows' in node
}

export function isDefinitionListNode(
  node: ParsedNode
): node is DefinitionListNode {
  return (
    node.type === 'definition_list' &&
    'items' in node &&
    Array.isArray(node.items)
  )
}

export function isMathBlockNode(node: ParsedNode): node is MathBlockNode {
  return node.type === 'math_block' && 'content' in node
}

export function isMathInlineNode(node: ParsedNode): node is MathInlineNode {
  return node.type === 'math_inline' && 'content' in node
}

export function isFootnoteNode(node: ParsedNode): node is FootnoteNode {
  return node.type === 'footnote' && 'id' in node && hasParsedChildren(node)
}

export function isHtmlBlockNode(node: ParsedNode): node is HtmlBlockNode {
  return node.type === 'html_block' && 'tag' in node
}
