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
import {
  buildAssertionResult,
  prepareCredentialRequestOptions,
} from '@/lib/passkey'
import { AuthOperationError } from '@/lib/secure-verification'

import type { PasskeyOptionsPayload } from './types'

const rememberedRPIDKey = 'passkey:last-successful-rp-id'

export interface PasskeyDomains {
  rpID?: string
  rpIDs: string[]
}

export interface PasskeySelection {
  rpID?: string
  onDomains?: (domains: PasskeyDomains) => void
}

export function rememberPasskeyRPID(rpID?: string) {
  if (!rpID) return
  try {
    localStorage.setItem(rememberedRPIDKey, rpID)
  } catch {
    // Storage is optional; it never authorizes a credential or a domain.
  }
}

export async function requestPasskeyAssertion(
  begin: (rpID?: string) => Promise<PasskeyOptionsPayload>,
  signal?: AbortSignal,
  selection: PasskeySelection = {}
): Promise<{
  flowToken: string
  assertion: Record<string, unknown>
  rpID?: string
}> {
  signal?.throwIfAborted()
  let remembered: string | undefined
  try {
    const value = localStorage.getItem(rememberedRPIDKey)
    if (value && value.length <= 253) remembered = value
  } catch {
    // Private browsing and blocked storage still support Passkey verification.
  }
  let payload: PasskeyOptionsPayload
  try {
    payload = await begin(selection.rpID ?? remembered)
  } catch (error) {
    const failure = AuthOperationError.from(error)
    if (
      selection.rpID ||
      !remembered ||
      failure.code !== 'PASSKEY_RP_ID_UNAVAILABLE'
    ) {
      throw failure
    }
    signal?.throwIfAborted()
    try {
      localStorage.removeItem(rememberedRPIDKey)
    } catch {
      /* optional storage */
    }
    // Retry a stale hint before opening a browser prompt. A dismissed prompt
    // or a failed finish request is never retried automatically.
    payload = await begin()
  }
  signal?.throwIfAborted()
  if (!payload.flow_token) {
    throw new AuthOperationError('Verification flow expired')
  }
  const publicKey = prepareCredentialRequestOptions(payload.options ?? payload)
  let rpIDs = publicKey.rpId ? [publicKey.rpId] : []
  if (Array.isArray(payload.rp_ids)) {
    rpIDs = payload.rp_ids.filter((id): id is string => typeof id === 'string')
  }
  selection.onDomains?.({ rpID: publicKey.rpId, rpIDs })
  let credential: PublicKeyCredential | null
  try {
    credential = (await navigator.credentials.get({
      publicKey,
      signal,
    })) as PublicKeyCredential | null
  } catch (error) {
    if (error instanceof DOMException && error.name === 'NotAllowedError') {
      throw new AuthOperationError(
        'Passkey verification was cancelled or timed out'
      )
    }
    throw error
  }
  signal?.throwIfAborted()
  if (!credential) {
    throw new AuthOperationError('Passkey verification was cancelled')
  }
  const assertion = buildAssertionResult(credential)
  if (!assertion) {
    throw new AuthOperationError('Unable to build Passkey assertion')
  }
  return { flowToken: payload.flow_token, assertion, rpID: publicKey.rpId }
}
