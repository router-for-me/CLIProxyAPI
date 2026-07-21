import React, { createContext, useCallback, useContext, useMemo, useRef, useState } from 'react';

// ToastProvider — lightweight, zero-dependency toast system.
//
// Renders a fixed top-right stack of auto-dismissing toasts. Any component
// inside the provider can call `const toast = useToast()` and then
// `toast.success('Key created')` / `toast.error('…')` / `toast.info('…')`.
//
// Each toast auto-dismisses after `duration` ms (default 3500). Errors stick
// a little longer (5000 ms) so operators can read them. Callers can pass an
// explicit `duration: 0` to keep a toast until dismissed manually.
const ToastContext = createContext(null);

let nextId = 1;

export function ToastProvider({ children }) {
  const [toasts, setToasts] = useState([]);
  // Keep a ref to the latest setter-bound remove fn so timers stay stable
  // across renders without effect churn.
  const timers = useRef(new Map());

  const remove = useCallback((id) => {
    setToasts((list) => list.filter((t) => t.id !== id));
    const handle = timers.current.get(id);
    if (handle) {
      clearTimeout(handle);
      timers.current.delete(id);
    }
  }, []);

  const push = useCallback((variant, message, opts = {}) => {
    const id = nextId++;
    const duration = opts.duration ?? (variant === 'error' ? 5000 : 3500);
    setToasts((list) => [...list, { id, variant, message }]);
    if (duration > 0) {
      const handle = setTimeout(() => remove(id), duration);
      timers.current.set(id, handle);
    }
    return id;
  }, [remove]);

  const api = useMemo(() => ({
    success: (msg, opts) => push('success', msg, opts),
    error: (msg, opts) => push('error', msg, opts),
    info: (msg, opts) => push('info', msg, opts),
    dismiss: remove,
  }), [push, remove]);

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div className="toast-stack" role="status" aria-live="polite">
        {toasts.map((t) => (
          <Toast key={t.id} variant={t.variant} message={t.message} onClose={() => remove(t.id)} />
        ))}
      </div>
    </ToastContext.Provider>
  );
}

function Toast({ variant, message, onClose }) {
  return (
    <div className={`toast toast--${variant}`}>
      <span className="toast__msg">{message}</span>
      <button className="toast__close" onClick={onClose} aria-label="Dismiss" type="button">×</button>
    </div>
  );
}

export function useToast() {
  const ctx = useContext(ToastContext);
  if (!ctx) {
    // Graceful no-op when used outside a provider (e.g. in isolated unit
    // tests). Keeps callers from crashing if the provider is absent.
    return {
      success() {}, error() {}, info() {}, dismiss() {},
    };
  }
  return ctx;
}
