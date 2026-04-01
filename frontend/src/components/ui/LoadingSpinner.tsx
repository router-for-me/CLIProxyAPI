export function LoadingSpinner({
  size = 20,
  className = ''
}: {
  size?: number;
  className?: string;
}) {
  return (
    <div
      className={`loading-spinner${className ? ` ${className}` : ''}`}
      style={{ width: size, height: size, borderWidth: size / 7 }}
      role="status"
      aria-live="polite"
    />
  );
}
