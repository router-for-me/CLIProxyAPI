// Conversation export helpers. Pure functions — the component layer
// calls these and triggers a Blob download.

export function toJsonExport(messages) {
  return JSON.stringify(messages, null, 2);
}

export function toMarkdownExport(messages) {
  const lines = ['# Playground Conversation', ''];
  for (const m of messages) {
    if (m.role === 'user') {
      lines.push('## User', '', m.content, '');
    } else if (m.role === 'assistant') {
      lines.push('## Assistant', '', m.content || '', '');
      if (m.usage) {
        lines.push(`_Usage: prompt ${m.usage.prompt_tokens ?? 0}, completion ${m.usage.completion_tokens ?? 0}_`, '');
      }
    }
  }
  return lines.join('\n');
}
