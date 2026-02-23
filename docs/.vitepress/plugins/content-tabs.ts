import type MarkdownIt from 'markdown-it'
import type { RuleBlock } from 'markdown-it/lib/parser_block'

/**
 * Parse tab definitions from markdown content
 *
 * Expected format:
 * ::: tabs
 * ::: tab python
 * ```python
 * print("hello")
 * ```
 * :::
 * ::: tab javascript
 * ```javascript
 * console.log("hello")
 * ```
 * :::
 * :::
 */
function parseTabsContent(content: string): { tabs: Array<{id: string, label: string, content: string}> } {
  const tabs: Array<{id: string, label: string, content: string}> = []
  const lines = content.split(/\r?\n/)
  let inTab = false
  let currentId = ''
  let currentContent: string[] = []

  const tabStart = /^\s*:::\s*tab\s+(.+?)\s*$/
  const tabEnd = /^\s*:::\s*$/

  for (const line of lines) {
    const startMatch = line.match(tabStart)
    if (startMatch) {
      if (inTab && currentContent.length > 0) {
        const content = currentContent.join('\n').trim()
        tabs.push({ id: currentId, label: currentId, content })
      }

      inTab = true
      currentId = startMatch[1].trim()
      currentContent = []
      continue
    }

    if (inTab && tabEnd.test(line)) {
      const content = currentContent.join('\n').trim()
      tabs.push({ id: currentId, label: currentId, content })
      inTab = false
      currentId = ''
      currentContent = []
      continue
    }

    if (inTab) {
      currentContent.push(line)
    }
  }

  if (inTab && currentContent.length > 0) {
    const content = currentContent.join('\n').trim()
    tabs.push({ id: currentId, label: currentId, content })
  }

  return { tabs }
}

function normalizeTabId(rawId: string): string {
  return rawId
    .trim()
    .toLowerCase()
    .replace(/\s+/g, '-')
    .replace(/[^\w-]/g, '')
}

export function contentTabsPlugin(md: MarkdownIt) {
  const parseTabsBlock = (state: {
    src: string
    bMarks: number[]
    eMarks: number[]
    tShift: number[]
  }, startLine: number, endLine: number) => {
    const tabStart = /^\s*:::\s*tab\s+(.+?)\s*$/
    const tabsStart = /^\s*:::\s*tabs\s*$/
    const tabsEnd = /^\s*:::\s*$/

    let closingLine = -1
    let line = startLine + 1
    let depth = 1
    let inTab = false

    for (; line <= endLine; line++) {
      const lineStart = state.bMarks[line] + state.tShift[line]
      const lineEnd = state.eMarks[line]
      const lineContent = state.src.slice(lineStart, lineEnd)

      if (tabsStart.test(lineContent) && line !== startLine) {
        depth += 1
        continue
      }

      if (tabsEnd.test(lineContent)) {
        if (inTab) {
          inTab = false
          continue
        }

        if (depth <= 1) {
          closingLine = line
          break
        }

        depth -= 1
        continue
      }

      if (tabStart.test(lineContent)) {
        inTab = true
        continue
      }
    }

    if (closingLine === -1) {
      return { content: '', tabs: [], closingLine: -1 }
    }

    const rawContent = state.src.slice(
      state.bMarks[startLine + 1],
      state.bMarks[closingLine]
    )
    const { tabs } = parseTabsContent(rawContent)

    return { content: rawContent, tabs, closingLine }
  }

  // Create custom container for tabs
  const tabsContainer: RuleBlock = (state, startLine, endLine, silent) => {
    const start = state.bMarks[startLine] + state.tShift[startLine]
    const max = state.eMarks[startLine]
    const line = state.src.slice(start, max)

    // Check for ::: tabs opening
    if (!line.match(/^\s*:::\s*tabs\s*$/)) {
      return false
    }

    if (silent) {
      return true
    }

    // Find the closing :::
    const parsed = parseTabsBlock(state, startLine, endLine)
    const closingLine = parsed.closingLine
    const { tabs } = parsed

    if (closingLine === -1) {
      return false
    }

    // Get the content between opening and closing
    if (tabs.length === 0) {
      return false
    }

    // Generate a unique ID for this tabs instance
    const tabsId = `tabs-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`

    // Remove temporary HTML token from output and emit marker token only.

    // We need to render the component inline - use a simpler approach
    // Just mark the section with special markers that Vue can pick up
    const markerToken = state.push('tabs_marker', '', 0)
    markerToken.content = JSON.stringify({ tabs, tabsId })
    markerToken.map = [startLine, closingLine]
    state.line = closingLine + 1

    return true
  }

  // Add the plugin
  md.block.ruler.after('fence', 'content_tabs', tabsContainer, {
    alt: ['paragraph', 'reference', 'blockquote', 'list']
  })

  // Custom renderer for the marker
  md.renderer.rules.tabs_marker = (tokens, idx, options, env, self) => {
    const token = tokens[idx]
    try {
      const data = JSON.parse(token.content)
      const tabs = data.tabs.map((t: {id: string, label: string}) => {
        const id = normalizeTabId(t.id)
        return {
          id,
          label: t.label.charAt(0).toUpperCase() + t.label.slice(1)
        }
      })

      // Generate the Vue component HTML with pre-rendered content
      let html = `<div class="content-tabs-wrapper" data-tabs-id="${data.tabsId}">`
      html += `<div class="content-tabs" data-tabs='${JSON.stringify(tabs)}'>`
      html += `<div class="tab-headers">`

      tabs.forEach((tab: {id: string, label: string}, idx: number) => {
        const active = idx === 0 ? 'active' : ''
        html += `<button class="tab-header ${active}" data-tab="${tab.id}">${tab.label}</button>`
      })

      html += `</div>`
      html += `<div class="tab-bodies">`

      data.tabs.forEach((tab: {id: string, label: string, content: string}, idx: number) => {
        const display = idx === 0 ? 'block' : 'none'
        const normalizedId = normalizeTabId(tab.id)
        html += `<div class="tab-body" data-tab="${normalizedId}" style="display: ${display}">`
        html += md.render(tab.content)
        html += `</div>`
      })

      html += `</div></div></div>`

      return html
    } catch (e) {
      return `<div class="content-tabs-error">Error parsing tabs</div>`
    }
  }
}

// Client-side script to initialize tab behavior
export const tabsClientScript = `
document.addEventListener('DOMContentLoaded', () => {
  document.querySelectorAll('.content-tabs-wrapper').forEach(wrapper => {
    const headers = wrapper.querySelectorAll('.tab-header')
    const bodies = wrapper.querySelectorAll('.tab-body')

    headers.forEach(header => {
      header.addEventListener('click', () => {
        const tabId = header.getAttribute('data-tab')

        // Update active state
        headers.forEach(h => h.classList.remove('active'))
        header.classList.add('active')

        // Show/hide bodies
        bodies.forEach(body => {
          if (body.getAttribute('data-tab') === tabId) {
            body.style.display = 'block'
          } else {
            body.style.display = 'none'
          }
        })
      })

      header.addEventListener('keydown', (e) => {
        const currentIndex = Array.from(headers).indexOf(header)

        if (e.key === 'ArrowRight' || e.key === 'ArrowDown') {
          e.preventDefault()
          const nextIndex = (currentIndex + 1) % headers.length
          headers[nextIndex].click()
          headers[nextIndex].focus()
        } else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') {
          e.preventDefault()
          const prevIndex = (currentIndex - 1 + headers.length) % headers.length
          headers[prevIndex].click()
          headers[prevIndex].focus()
        } else if (e.key === 'Home') {
          e.preventDefault()
          headers[0].click()
          headers[0].focus()
        } else if (e.key === 'End') {
          e.preventDefault()
          headers[headers.length - 1].click()
          headers[headers.length - 1].focus()
        }
      })
    })
  })
})
`
