import { javascript } from '@codemirror/lang-javascript'
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
import { syntaxHighlighting } from '@codemirror/language'
import { EditorState } from '@codemirror/state'
import { EditorView, lineNumbers } from '@codemirror/view'
import { classHighlighter } from '@lezer/highlight'
import { useEffect, useRef } from 'react'

import { cn } from '@/lib/utils'

type JavaScriptViewerProps = {
  value: string
  className?: string
}

export function JavaScriptViewer(props: JavaScriptViewerProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)

  useEffect(() => {
    if (!containerRef.current) return
    const view = new EditorView({
      parent: containerRef.current,
      state: EditorState.create({
        doc: props.value,
        extensions: [
          lineNumbers(),
          javascript(),
          syntaxHighlighting(classHighlighter),
          EditorState.readOnly.of(true),
          EditorView.editable.of(false),
          EditorView.lineWrapping,
          EditorView.theme({
            '&': { height: '100%', backgroundColor: 'transparent' },
            '.cm-scroller': {
              overflow: 'auto',
              fontFamily: 'var(--font-mono)',
              lineHeight: '1.65',
            },
            '.cm-content': { padding: '12px 0' },
            '.cm-gutters': {
              backgroundColor: 'transparent',
              color: 'var(--muted-foreground)',
              borderColor: 'var(--border)',
            },
            '.cm-line': { padding: '0 12px' },
          }),
        ],
      }),
    })
    viewRef.current = view
    return () => {
      view.destroy()
      viewRef.current = null
    }
  }, [props.value])

  return (
    <div
      ref={containerRef}
      className={cn(
        '[&_.tok-keyword]:text-purple-700 dark:[&_.tok-keyword]:text-purple-300',
        '[&_.tok-string]:text-green-800 dark:[&_.tok-string]:text-green-300',
        '[&_.tok-number]:text-orange-800 dark:[&_.tok-number]:text-orange-300 [&_.tok-bool]:text-orange-800 dark:[&_.tok-bool]:text-orange-300',
        '[&_.tok-comment]:text-muted-foreground [&_.tok-comment]:italic',
        '[&_.tok-variableName.tok-function]:text-blue-700 dark:[&_.tok-variableName.tok-function]:text-blue-300',
        '[&_.tok-definition]:text-blue-700 dark:[&_.tok-definition]:text-blue-300',
        '[&_.tok-propertyName]:text-teal-800 dark:[&_.tok-propertyName]:text-teal-300',
        props.className
      )}
    />
  )
}
