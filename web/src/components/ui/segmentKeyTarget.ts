/**
 * The option a key moves focus and selection to, or -1. Follows the WAI-ARIA
 * radio group pattern: Down/Right go to the next enabled option and Up/Left
 * to the previous one, wrapping; Home and End go to the first and last.
 */
export const segmentKeyTarget = (
  key: string,
  index: number,
  count: number,
  isEnabled: (index: number) => boolean
): number => {
  const nearestEnabled = (from: number, step: 1 | -1) => {
    const walk = Array.from(
      { length: count },
      (_, offset) => (((from + step * offset) % count) + count) % count
    );

    return walk.find(isEnabled) ?? -1;
  };

  switch (key) {
    case 'ArrowRight':
    case 'ArrowDown':
      return nearestEnabled(index + 1, 1);
    case 'ArrowLeft':
    case 'ArrowUp':
      return nearestEnabled(index - 1, -1);
    case 'Home':
      return nearestEnabled(0, 1);
    case 'End':
      return nearestEnabled(count - 1, -1);
    default:
      return -1;
  }
};
