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
/**
 * Get message content styles based on role
 * Encapsulates styling logic for user and assistant messages
 */
export function getMessageContentStyles() {
  return [
    // Assistant content reads like a document column; user bubble stays compact.
    'group-[.is-assistant]:w-full',
    'group-[.is-assistant]:max-w-[78ch]',
    'group-[.is-user]:w-fit',

    // User bubble: compact surface that stays calm in both light and dark themes.
    'group-[.is-user]:rounded-2xl',
    'group-[.is-user]:rounded-br-md',
    'group-[.is-user]:border',
    'group-[.is-user]:border-border/70',
    'group-[.is-user]:bg-muted/70',
    'group-[.is-user]:px-4',
    'group-[.is-user]:py-2.5',
    'group-[.is-user]:text-foreground',
    'group-[.is-user]:shadow-sm',
    'group-[.is-user]:shadow-black/5',

    // Assistant response: flat reading surface using the active UI font axis.
    'group-[.is-assistant]:bg-transparent',
    'group-[.is-assistant]:p-0',
    'group-[.is-assistant]:rounded-none',
    'group-[.is-assistant]:overflow-visible',
    'group-[.is-assistant]:[font-family:var(--font-body)]',
    'group-[.is-assistant]:text-foreground/90',

    // Preferred readable widths and wrapping
    'text-[0.95rem]',
    'leading-6',
    'break-words',
    'whitespace-pre-wrap',
    'sm:text-[0.975rem]',
    'sm:leading-7',

    // Cap user bubble width so it does not look like a banner
    'group-[.is-user]:max-w-[85%]',
    'sm:group-[.is-user]:max-w-[62ch]',
    'md:group-[.is-user]:max-w-[68ch]',
    'lg:group-[.is-user]:max-w-[72ch]',
  ].join(' ')
}
