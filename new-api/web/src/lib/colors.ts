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
export type SemanticColor =
  | 'blue'
  | 'green'
  | 'cyan'
  | 'purple'
  | 'pink'
  | 'red'
  | 'orange'
  | 'amber'
  | 'yellow'
  | 'lime'
  | 'light-green'
  | 'teal'
  | 'light-blue'
  | 'indigo'
  | 'violet'
  | 'grey'
  | 'slate'

export const colorToBgClass: Record<SemanticColor, string> = {
  blue: 'bg-blue-500',
  green: 'bg-green-500',
  cyan: 'bg-cyan-500',
  purple: 'bg-purple-500',
  pink: 'bg-pink-500',
  red: 'bg-red-500',
  orange: 'bg-orange-500',
  amber: 'bg-amber-500',
  yellow: 'bg-yellow-500',
  lime: 'bg-lime-500',
  'light-green': 'bg-green-400',
  teal: 'bg-teal-500',
  'light-blue': 'bg-sky-400',
  indigo: 'bg-indigo-500',
  violet: 'bg-violet-500',
  grey: 'bg-gray-400',
  slate: 'bg-slate-500',
}

export const avatarColorMap: Record<SemanticColor, string> = {
  blue: 'bg-chart-1/10 text-chart-1',
  green: 'bg-success/10 text-success',
  cyan: 'bg-chart-2/10 text-chart-2',
  purple: 'bg-chart-4/10 text-chart-4',
  pink: 'bg-chart-5/10 text-chart-5',
  red: 'bg-destructive/10 text-destructive',
  orange: 'bg-warning/10 text-warning',
  amber: 'bg-warning/10 text-warning',
  yellow: 'bg-warning/10 text-warning',
  lime: 'bg-chart-3/10 text-chart-3',
  'light-green': 'bg-success/10 text-success',
  teal: 'bg-chart-2/10 text-chart-2',
  'light-blue': 'bg-info/10 text-info',
  indigo: 'bg-chart-1/10 text-chart-1',
  violet: 'bg-chart-4/10 text-chart-4',
  grey: 'bg-muted text-muted-foreground',
  slate: 'bg-muted text-muted-foreground',
}

export function getAvatarColorClass(name: string): string {
  return avatarColorMap[stringToColor(name)]
}

export function getBgColorClass(color?: string): string {
  if (!color) return colorToBgClass.blue
  return (
    (colorToBgClass as Record<string, string>)[color] || colorToBgClass.blue
  )
}

/**
 * Chart color palette - Modern gradient colors compatible with light/dark themes
 * Uses HSL format for better theme adaptation
 */
export const CHART_COLORS = [
  'hsl(217, 91%, 60%)', // blue
  'hsl(142, 76%, 36%)', // green
  'hsl(38, 92%, 50%)', // amber
  'hsl(258, 90%, 66%)', // violet
  'hsl(330, 81%, 60%)', // pink
  'hsl(189, 94%, 43%)', // cyan
  'hsl(25, 95%, 53%)', // orange
  'hsl(239, 84%, 67%)', // indigo
  'hsl(173, 80%, 40%)', // teal
  'hsl(271, 91%, 65%)', // purple
  'hsl(199, 89%, 48%)', // sky
  'hsl(280, 65%, 60%)', // fuchsia
] as const

/**
 * Get a chart color by index (cycles through the palette)
 */
export function getChartColor(index: number): string {
  return CHART_COLORS[index % CHART_COLORS.length]
}

/**
 * Announcement status types
 */
export type AnnouncementType =
  | 'default'
  | 'ongoing'
  | 'success'
  | 'warning'
  | 'error'

/**
 * Announcement status color mapping
 */
export const ANNOUNCEMENT_TYPE_COLORS: Record<AnnouncementType, string> = {
  default: 'bg-neutral',
  ongoing: 'bg-info',
  success: 'bg-success',
  warning: 'bg-warning',
  error: 'bg-destructive',
}

/**
 * Get announcement status color class
 */
export function getAnnouncementColorClass(type?: string): string {
  const validType = (type || 'default') as AnnouncementType
  return ANNOUNCEMENT_TYPE_COLORS[validType] || ANNOUNCEMENT_TYPE_COLORS.default
}

/**
 * Semantic colors for tags and badges
 */
const TAG_COLORS = [
  'amber',
  'blue',
  'cyan',
  'green',
  'grey',
  'indigo',
  'light-blue',
  'lime',
  'orange',
  'pink',
  'purple',
  'red',
  'teal',
  'violet',
  'yellow',
] as const

/**
 * Convert string to a stable semantic color
 * Used for model tags, group badges, user avatars, etc.
 * Same string always returns the same color
 *
 * @param str - Input string (model name, group name, username, etc.)
 * @returns Semantic color name from TAG_COLORS
 *
 * @example
 * stringToColor('gpt-4') // 'blue'
 * stringToColor('claude-3') // 'purple'
 * stringToColor('default') // 'green'
 */
export function stringToColor(str: string): SemanticColor {
  let sum = 0
  for (let i = 0; i < str.length; i++) {
    sum += str.charCodeAt(i)
  }
  const index = sum % TAG_COLORS.length
  return TAG_COLORS[index]
}
