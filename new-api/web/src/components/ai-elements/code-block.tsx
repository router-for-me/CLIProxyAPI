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
/* eslint-disable react-refresh/only-export-components */
'use client'

import { javascript } from '@codemirror/lang-javascript'
import { markdown } from '@codemirror/lang-markdown'
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language'
import { EditorState, type Extension } from '@codemirror/state'
import {
  EditorView,
  lineNumbers,
  placeholder as placeholderExtension,
} from '@codemirror/view'
import { tags as highlightTags } from '@lezer/highlight'
import {
  CheckIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  CopyIcon,
  DownloadIcon,
} from 'lucide-react'
import {
  type ComponentProps,
  createContext,
  type CSSProperties,
  type HTMLAttributes,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'
import type { BundledLanguage } from 'shiki'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

type CodeBlockProps = HTMLAttributes<HTMLDivElement> & {
  code: string
  collapsedLines?: number
  defaultCollapsed?: boolean
  enableCollapse?: boolean
  filename?: string
  language: BundledLanguage | string
  maxExpandedLines?: number
  /** @deprecated use collapsedLines for collapsed preview height. */
  maxCollapsedLines?: number
  showLineNumbers?: boolean
  showToolbar?: boolean
  title?: ReactNode
}

type CodeBlockEditorProps = Omit<
  HTMLAttributes<HTMLDivElement>,
  'onChange' | 'onKeyDown' | 'title'
> & {
  actions?: ReactNode
  ariaLabel: string
  autoFocus?: boolean
  language: BundledLanguage | string
  onChange: (value: string) => void
  onKeyDown?: (event: globalThis.KeyboardEvent) => void
  placeholder?: string
  rows?: number
  title?: ReactNode
  value: string
}

type CodeMirrorCodeViewProps = {
  ariaLabel: string
  autoFocus?: boolean
  language: BundledLanguage | string
  onChange?: (value: string) => void
  onKeyDown?: (event: globalThis.KeyboardEvent) => void
  placeholder?: string
  readOnly?: boolean
  rows?: number
  showLineNumbers?: boolean
  value: string
}

type CodeBlockFrameProps = Omit<HTMLAttributes<HTMLDivElement>, 'title'> & {
  bodyClassName?: string
  bodyMaxHeight?: string
  bodyOverlay?: ReactNode
  children: ReactNode
  endActions?: ReactNode
  showToolbar?: boolean
  title?: ReactNode
}

type CodeBlockContextType = {
  code: string
  language: string
}

const CodeBlockContext = createContext<CodeBlockContextType>({
  code: '',
  language: 'plaintext',
})

const LANGUAGE_ALIASES: Record<string, BundledLanguage> = {
  csharp: 'c#',
  golang: 'go',
  js: 'javascript',
  shell: 'bash',
  shellscript: 'bash',
  ts: 'typescript',
}

const LANGUAGE_PATTERN = /^[a-z0-9][a-z0-9+#._-]{0,31}$/i
const codeMirrorTheme = EditorView.theme({
  '&': {
    background: 'transparent',
    color: 'var(--foreground)',
    fontSize: '13px',
  },
  '.cm-content': {
    caretColor: 'var(--foreground)',
    fontFamily: 'var(--font-mono)',
    lineHeight: '1.5rem',
    minHeight: 'var(--code-editor-min-height)',
    minWidth: 'max-content',
    padding: '1rem 1rem 1rem 0',
  },
  '.cm-editor': {
    background: 'transparent',
    width: '100%',
  },
  '.cm-focused': {
    outline: 'none',
  },
  '.cm-gutters': {
    background: 'transparent',
    borderRight: '0',
    color: 'var(--muted-foreground)',
    fontFamily: 'var(--font-mono)',
    fontSize: '13px',
    lineHeight: '1.5rem',
    // CodeMirror already accounts for the content's vertical padding.
    padding: '0 1rem 0 0',
  },
  '.cm-gutters:empty': {
    display: 'none',
  },
  '.cm-lineNumbers .cm-gutterElement': {
    minWidth: '2.5rem',
    padding: '0 1rem 0 0',
    textAlign: 'right',
  },
  '.cm-line': {
    padding: '0',
  },
  '.cm-scroller': {
    fontFamily: 'var(--font-mono)',
    lineHeight: '1.5rem',
    minHeight: 'var(--code-editor-min-height)',
    overflow: 'auto',
  },
  '.cm-selectionBackground': {
    background:
      'color-mix(in oklch, var(--primary) 28%, transparent) !important',
  },
})

const codeMirrorHighlightStyle = syntaxHighlighting(
  HighlightStyle.define([
    { tag: highlightTags.heading, color: '#e06c75', fontWeight: '600' },
    { tag: [highlightTags.strong, highlightTags.emphasis], color: '#d19a66' },
    { tag: [highlightTags.link, highlightTags.url], color: '#61afef' },
    {
      tag: [highlightTags.monospace, highlightTags.contentSeparator],
      color: '#98c379',
    },
    {
      tag: [highlightTags.keyword, highlightTags.processingInstruction],
      color: '#c678dd',
    },
    {
      tag: [highlightTags.atom, highlightTags.bool, highlightTags.number],
      color: '#d19a66',
    },
    { tag: [highlightTags.string, highlightTags.inserted], color: '#98c379' },
    { tag: [highlightTags.deleted, highlightTags.invalid], color: '#e06c75' },
    {
      tag: [highlightTags.meta, highlightTags.comment],
      color: 'var(--muted-foreground)',
    },
  ])
)

function getRequestedCodeLanguage(language?: string) {
  const normalized = language?.trim().toLowerCase() || 'plaintext'
  if (!LANGUAGE_PATTERN.test(normalized)) {
    return 'plaintext'
  }

  return LANGUAGE_ALIASES[normalized] ?? normalized
}

function getCodeMirrorLanguageExtension(language: BundledLanguage | string) {
  const requestedLanguage = getRequestedCodeLanguage(language)
  if (
    requestedLanguage === 'markdown' ||
    requestedLanguage === 'md' ||
    requestedLanguage === 'mdx'
  ) {
    return markdown()
  }

  if (requestedLanguage === 'javascript' || requestedLanguage === 'jsx') {
    return javascript({ jsx: requestedLanguage === 'jsx' })
  }

  if (requestedLanguage === 'typescript' || requestedLanguage === 'tsx') {
    return javascript({ jsx: requestedLanguage === 'tsx', typescript: true })
  }

  return []
}

function getCodeLineCount(code: string) {
  if (!code) {
    return 1
  }

  return code.split('\n').length
}

function getDownloadFilename(language: string, filename?: string) {
  if (filename) {
    return filename
  }

  const extension = language === 'plaintext' ? 'txt' : language
  return `code.${extension}`
}

function getCodeBlockHeight(lines: number) {
  return `${Math.max(4, lines) * 1.5 + 2}rem`
}

function getCodeBlockMaxHeight(
  isCodeCollapsed: boolean,
  previewLines: number,
  maxExpandedLines?: number
): string | undefined {
  if (isCodeCollapsed) {
    return getCodeBlockHeight(previewLines)
  }

  if (maxExpandedLines) {
    return getCodeBlockHeight(maxExpandedLines)
  }

  return undefined
}

function getCodeMirrorExtensions(options: {
  language: BundledLanguage | string
  onKeyDown: (event: globalThis.KeyboardEvent) => void
  placeholder?: string
  readOnly: boolean
  showLineNumbers: boolean
}): Extension[] {
  const extensions: Extension[] = [
    getCodeMirrorLanguageExtension(options.language),
    codeMirrorHighlightStyle,
    codeMirrorTheme,
    EditorState.tabSize.of(2),
    EditorState.readOnly.of(options.readOnly),
    EditorView.editable.of(!options.readOnly),
    EditorView.domEventHandlers({
      keydown(event) {
        options.onKeyDown(event)
        return event.defaultPrevented
      },
    }),
  ]

  if (options.placeholder) {
    extensions.push(placeholderExtension(options.placeholder))
  }

  if (options.showLineNumbers) {
    extensions.unshift(lineNumbers())
  }

  return extensions
}

function CodeMirrorCodeView({
  ariaLabel,
  autoFocus = false,
  language,
  onChange,
  onKeyDown,
  placeholder,
  readOnly = false,
  rows = 8,
  showLineNumbers = true,
  value,
}: CodeMirrorCodeViewProps) {
  const editorHostRef = useRef<HTMLDivElement>(null)
  const editorViewRef = useRef<EditorView | null>(null)
  const initialValueRef = useRef(value)
  const onChangeRef = useRef(onChange)
  const onKeyDownRef = useRef(onKeyDown)
  const editorMinHeight = `${Math.max(4, rows) * 1.5 + 2}rem`
  // onKeyDown is delivered through a ref so a new handler identity from the
  // parent (recreated on every keystroke-driven render) does not invalidate
  // the extensions and tear down the EditorView, which would reset the cursor
  // to the document start and make typing appear right-to-left.
  const editorExtensions = useMemo(
    () =>
      getCodeMirrorExtensions({
        language,
        onKeyDown: (event) => onKeyDownRef.current?.(event),
        placeholder,
        readOnly,
        showLineNumbers,
      }),
    [language, placeholder, readOnly, showLineNumbers]
  )

  useEffect(() => {
    onChangeRef.current = onChange
    onKeyDownRef.current = onKeyDown
  }, [onChange, onKeyDown])

  useEffect(() => {
    const editorHost = editorHostRef.current
    if (!editorHost) {
      return
    }

    const editorView = new EditorView({
      doc: initialValueRef.current,
      extensions: [
        ...editorExtensions,
        EditorView.updateListener.of((update) => {
          if (update.docChanged) {
            onChangeRef.current?.(update.state.doc.toString())
          }
        }),
      ],
      parent: editorHost,
    })
    editorViewRef.current = editorView
    if (autoFocus) {
      editorView.focus()
    }

    return () => {
      editorView.destroy()
      editorViewRef.current = null
    }
  }, [autoFocus, editorExtensions])

  useEffect(() => {
    // Track the latest value so a future editor rebuild (e.g. language change)
    // starts from the current document instead of the mount-time snapshot.
    initialValueRef.current = value

    const editorView = editorViewRef.current
    if (!editorView) {
      return
    }

    const currentValue = editorView.state.doc.toString()
    if (currentValue === value) {
      return
    }

    editorView.dispatch({
      changes: {
        from: 0,
        to: editorView.state.doc.length,
        insert: value,
      },
    })
  }, [value])

  return (
    <div
      aria-label={ariaLabel}
      aria-readonly={readOnly}
      className='min-h-(--code-editor-min-height)'
      ref={editorHostRef}
      role='textbox'
      style={
        {
          '--code-editor-min-height': editorMinHeight,
        } as CSSProperties
      }
    />
  )
}

export const CodeBlockFrame = ({
  bodyClassName,
  bodyMaxHeight,
  bodyOverlay,
  children,
  className,
  endActions,
  showToolbar = false,
  title,
  ...props
}: CodeBlockFrameProps) => (
  <div
    className={cn(
      'group/code-block bg-muted/20 text-foreground my-3 w-full max-w-full overflow-hidden rounded-lg border shadow-xs',
      className
    )}
    {...props}
  >
    {showToolbar && (
      <div className='bg-muted/35 border-border/70 flex min-h-10 items-center gap-2 border-b px-2 py-1.5'>
        <div className='min-w-0 flex-1'>
          <div className='text-muted-foreground truncate font-mono text-[11px] font-medium tracking-wide uppercase'>
            {title}
          </div>
        </div>
        {endActions && (
          <div className='flex shrink-0 items-center gap-1'>{endActions}</div>
        )}
      </div>
    )}
    <div className='relative min-w-0'>
      <div
        className={cn(
          'code-block-scroll max-w-full overflow-auto transition-[max-height] duration-200 ease-out',
          bodyClassName
        )}
        style={{ maxHeight: bodyMaxHeight }}
      >
        {children}
      </div>
      {bodyOverlay}
    </div>
  </div>
)

export const CodeBlock = ({
  code,
  collapsedLines = 12,
  defaultCollapsed,
  enableCollapse = true,
  filename,
  language,
  maxExpandedLines,
  maxCollapsedLines,
  showLineNumbers = false,
  showToolbar = false,
  title,
  className,
  children,
  ...props
}: CodeBlockProps) => {
  const { t } = useTranslation()
  const [isCollapsed, setIsCollapsed] = useState(Boolean(defaultCollapsed))
  const displayLanguage = getRequestedCodeLanguage(language)
  const lineCount = useMemo(() => getCodeLineCount(code), [code])
  const previewLines = maxCollapsedLines ?? collapsedLines
  const canCollapse = enableCollapse && lineCount > previewLines
  const isCodeCollapsed = canCollapse && isCollapsed
  const displayTitle = title ?? displayLanguage
  const bodyMaxHeight = getCodeBlockMaxHeight(
    isCodeCollapsed,
    previewLines,
    maxExpandedLines
  )

  const downloadCode = () => {
    if (typeof window === 'undefined') {
      return
    }

    const blob = new Blob([code], { type: 'text/plain;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = getDownloadFilename(displayLanguage, filename)
    anchor.click()
    URL.revokeObjectURL(url)
  }

  return (
    <CodeBlockContext.Provider value={{ code, language: displayLanguage }}>
      <CodeBlockFrame
        bodyClassName='p-0'
        bodyMaxHeight={bodyMaxHeight}
        bodyOverlay={
          <>
            {isCodeCollapsed && (
              <div className='from-muted/20 to-background pointer-events-none absolute inset-x-0 bottom-0 h-16 bg-linear-to-b' />
            )}
            {!showToolbar && children && (
              <div className='absolute top-2 right-2 flex items-center gap-1'>
                {children}
              </div>
            )}
          </>
        }
        className={className}
        endActions={
          <>
            {canCollapse && (
              <Tooltip>
                <TooltipTrigger
                  render={
                    <Button
                      aria-label={isCodeCollapsed ? t('Expand') : t('Collapse')}
                      className='size-8'
                      onClick={() => setIsCollapsed((value) => !value)}
                      size='icon-sm'
                      type='button'
                      variant='ghost'
                    >
                      {isCodeCollapsed ? (
                        <ChevronRightIcon className='size-4' />
                      ) : (
                        <ChevronDownIcon className='size-4' />
                      )}
                    </Button>
                  }
                />
                <TooltipContent>
                  <p>{isCodeCollapsed ? t('Expand') : t('Collapse')}</p>
                </TooltipContent>
              </Tooltip>
            )}
            {showToolbar && children}
            <Tooltip>
              <TooltipTrigger
                render={
                  <Button
                    aria-label={t('Download')}
                    className='size-8'
                    onClick={downloadCode}
                    size='icon-sm'
                    type='button'
                    variant='ghost'
                  >
                    <DownloadIcon className='size-4' />
                  </Button>
                }
              />
              <TooltipContent>
                <p>{t('Download')}</p>
              </TooltipContent>
            </Tooltip>
          </>
        }
        showToolbar={showToolbar}
        title={displayTitle}
        {...props}
      >
        <CodeMirrorCodeView
          ariaLabel={
            typeof displayTitle === 'string' ? displayTitle : displayLanguage
          }
          language={language}
          readOnly
          rows={Math.min(Math.max(lineCount, 4), maxExpandedLines ?? lineCount)}
          showLineNumbers={showLineNumbers}
          value={code}
        />
      </CodeBlockFrame>
    </CodeBlockContext.Provider>
  )
}

export const CodeBlockEditor = ({
  actions,
  ariaLabel,
  autoFocus = true,
  className,
  language,
  onChange,
  onKeyDown,
  placeholder,
  rows = 8,
  title,
  value,
  ...props
}: CodeBlockEditorProps) => {
  return (
    <CodeBlockFrame
      bodyClassName='p-0'
      className={className}
      endActions={actions}
      showToolbar
      title={title}
      {...props}
    >
      <CodeMirrorCodeView
        ariaLabel={ariaLabel}
        autoFocus={autoFocus}
        language={language}
        onChange={onChange}
        onKeyDown={onKeyDown}
        placeholder={placeholder}
        rows={rows}
        showLineNumbers
        value={value}
      />
    </CodeBlockFrame>
  )
}

export type CodeBlockCopyButtonProps = ComponentProps<typeof Button> & {
  onCopy?: () => void
  onError?: (error: Error) => void
  timeout?: number
}

export const CodeBlockCopyButton = ({
  onCopy,
  onError,
  timeout = 2000,
  children,
  className,
  ...props
}: CodeBlockCopyButtonProps) => {
  const { t } = useTranslation()
  const [isCopied, setIsCopied] = useState(false)
  const { code } = useContext(CodeBlockContext)

  const copyToClipboard = async () => {
    if (typeof window === 'undefined' || !navigator?.clipboard?.writeText) {
      onError?.(new Error('Clipboard API not available'))
      return
    }

    try {
      await navigator.clipboard.writeText(code)
      setIsCopied(true)
      onCopy?.()
      setTimeout(() => setIsCopied(false), timeout)
    } catch (error) {
      onError?.(error as Error)
    }
  }

  const Icon = isCopied ? CheckIcon : CopyIcon

  const button = (
    <Button
      aria-label={isCopied ? t('Copied!') : t('Copy code')}
      className={cn('size-8 shrink-0', className)}
      onClick={copyToClipboard}
      size='icon-sm'
      type='button'
      variant='ghost'
      {...props}
    >
      {children ?? <Icon size={14} />}
    </Button>
  )

  return (
    <Tooltip>
      <TooltipTrigger render={button} />
      <TooltipContent>
        <p>{isCopied ? t('Copied!') : t('Copy code')}</p>
      </TooltipContent>
    </Tooltip>
  )
}
