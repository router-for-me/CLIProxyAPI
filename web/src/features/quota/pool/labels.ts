export interface Rect {
  height: number;
  width: number;
  x: number;
  y: number;
}

export interface LabelCandidate extends Rect {
  id: string;
  priority: number;
}

/** Rough text width for placement: 0.58em per character plus 6px of padding. */
export const estimateLabelWidth = (text: string, fontPx = 12): number =>
  text.length * 0.58 * fontPx + 6;

const overlaps = (left: Rect, right: Rect): boolean =>
  left.x < right.x + right.width &&
  right.x < left.x + left.width &&
  left.y < right.y + right.height &&
  right.y < left.y + left.height;

const isInside = (rect: Rect, bounds: Rect): boolean =>
  rect.x >= bounds.x &&
  rect.y >= bounds.y &&
  rect.x + rect.width <= bounds.x + bounds.width &&
  rect.y + rect.height <= bounds.y + bounds.height;

/**
 * Greedy placement by priority (descending, input order on ties). A candidate
 * is placed when it lies inside `bounds` and overlaps neither a reserved rect
 * nor a placed label. Later candidates with an already placed id are skipped,
 * so alternative positions of one label can share its id.
 */
export const placeLabels = (
  candidates: LabelCandidate[],
  reserved: Rect[],
  bounds: Rect
): Set<string> => {
  const placedIds = new Set<string>();
  const occupied = [...reserved];
  const ordered = [...candidates].sort((left, right) => right.priority - left.priority);

  for (const candidate of ordered) {
    const isFree =
      !placedIds.has(candidate.id) &&
      isInside(candidate, bounds) &&
      !occupied.some((rect) => overlaps(rect, candidate));

    if (isFree) {
      placedIds.add(candidate.id);
      occupied.push(candidate);
    }
  }

  return placedIds;
};
