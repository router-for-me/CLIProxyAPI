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
 * Detection of the server's login-session hint cookie.
 *
 * The Refresh Cookie is `HttpOnly` and scoped to `/api/user/auth`, so a page at
 * `/` cannot read it and cannot tell an anonymous visitor from a returning one.
 * The server therefore writes `new_api_has_session=1` alongside it — same
 * expiry, `Path=/`, not `HttpOnly` — purely so the frontend can skip a refresh
 * that is certain to fail.
 *
 * This is an optimization, never an authorization signal. A present hint means
 * "a Refresh Cookie was issued at some point"; it can be stale after a
 * server-side revocation, and it can be absent while a usable Refresh Cookie
 * still exists (a visitor who cleared site data for `/` only, or any session
 * created before this cookie shipped). Callers must treat a missing hint as
 * "not worth a request right now", not as "signed out", and must still be able
 * to reach the server when authentication actually matters.
 */
export const SESSION_HINT_COOKIE_NAME = 'new_api_has_session'

/** Read a cookie value out of a `document.cookie`-shaped string. */
export function readCookie(cookieHeader: string, name: string): string | null {
  for (const part of cookieHeader.split(';')) {
    const separator = part.indexOf('=')
    if (separator < 0) continue
    if (part.slice(0, separator).trim() !== name) continue
    return part.slice(separator + 1).trim()
  }
  return null
}

/**
 * Whether the server currently claims a login session exists.
 *
 * Returns `true` when the hint cannot be read at all (no `document`, as in SSR
 * or a non-DOM test environment). An unreadable hint is not evidence of
 * absence, and the safe direction is to let the refresh proceed.
 */
export function hasSessionHint(): boolean {
  if (typeof document === 'undefined') return true
  return (
    readCookie(document.cookie, SESSION_HINT_COOKIE_NAME) !== null
  )
}
