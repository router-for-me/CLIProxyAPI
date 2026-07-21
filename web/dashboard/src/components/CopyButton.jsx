import React, { useCallback, useRef, useState } from 'react';

// CopyButton — one-click copy-to-clipboard with a transient "Copied" state.
//
// Renders a small button that copies `value` to the clipboard on click and
// flips to a confirmation label/icon for `resetMs` (default 1500 ms). Falls
// back to a hidden-textarea execCommand('copy') path when the async
// Clipboard API is unavailable (older browsers / non-secure contexts).
export default function CopyButton({
  value,
  label = 'Copy',
  copiedLabel = 'Copied!',
  small = true,
  resetMs = 1500,
  className = '',
}) {
  const [copied, setCopied] = useState(false);
  const timer = useRef(null);

  const handleCopy = useCallback(async (e) => {
    e?.stopPropagation();
    const text = typeof value === 'function' ? value() : value;
    if (!text) return;
    try {
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text);
      } else {
        const ta = document.createElement('textarea');
        ta.value = text;
        ta.style.position = 'fixed';
        ta.style.opacity = '0';
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
      }
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), resetMs);
    } catch {
      // Silently ignore — the button just won't flip to "Copied".
    }
  }, [value, resetMs]);

  return (
    <button
      type="button"
      onClick={handleCopy}
      className={`copy-btn ${copied ? 'copy-btn--copied' : ''} ${small ? 'copy-btn--sm' : ''} ${className}`}
      title={copied ? copiedLabel : label}
      aria-label={label}
    >
      {copied ? copiedLabel : label}
    </button>
  );
}
