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
import { api } from '@/lib/api'
import { authRequestOptions, authResult } from '@/lib/secure-verification'

import type {
  SecurityProof,
  VerificationOperation,
} from '../secure-verification/types'
import type { ApiResponse, PasskeyOptionsPayload, PasskeyStatus } from './types'

function proofHeaders(proofToken?: string): Record<string, string> | undefined {
  return proofToken ? { 'X-Security-Proof': proofToken } : undefined
}

export async function getPasskeyStatus(): Promise<ApiResponse<PasskeyStatus>> {
  const res = await api.get<ApiResponse<PasskeyStatus>>('/api/user/passkey')
  return res.data
}

export function beginPasskeyRegistration(
  proofToken: string,
  signal?: AbortSignal
): Promise<PasskeyOptionsPayload> {
  return authResult(
    api.post('/api/user/passkey/register/begin', undefined, {
      ...authRequestOptions,
      headers: proofHeaders(proofToken),
      signal,
    })
  )
}

export function finishPasskeyRegistration(
  flowToken: string,
  payload: Record<string, unknown>,
  signal?: AbortSignal
): Promise<unknown> {
  return authResult(
    api.post(
      '/api/user/passkey/register/finish',
      { flow_token: flowToken, credential: payload },
      {
        ...authRequestOptions,
        acceptAuthRotation: true,
        singleUseAuthorization: true,
        signal,
      }
    )
  )
}

export function deletePasskey(
  proofToken: string,
  signal?: AbortSignal
): Promise<unknown> {
  return authResult(
    api.delete('/api/user/passkey', {
      ...authRequestOptions,
      headers: proofHeaders(proofToken),
      acceptAuthRotation: true,
      signal,
    })
  )
}

export function beginPasskeyLogin(
  rpID?: string,
  signal?: AbortSignal
): Promise<PasskeyOptionsPayload> {
  return authResult(
    api.post<ApiResponse<PasskeyOptionsPayload>>(
      '/api/user/passkey/login/begin',
      rpID ? { rp_id: rpID } : undefined,
      { ...authRequestOptions, signal, skipAuthRefresh: true }
    )
  )
}

export async function finishPasskeyLogin(
  flowToken: string,
  payload: Record<string, unknown>,
  signal?: AbortSignal
): Promise<ApiResponse> {
  const res = await api.post<ApiResponse>(
    '/api/user/passkey/login/finish',
    { flow_token: flowToken, credential: payload },
    { skipAuthRefresh: true, signal }
  )
  return res.data
}

export function beginPasskeyVerification(
  operation: VerificationOperation,
  signal?: AbortSignal,
  rpID?: string
): Promise<PasskeyOptionsPayload> {
  return authResult(
    api.post(
      '/api/user/passkey/verify/begin',
      {
        scope: operation.scope,
        ...(rpID ? { rp_id: rpID } : {}),
        ...(operation.context ? { context: operation.context } : {}),
      },
      { ...authRequestOptions, signal }
    )
  )
}

export function finishPasskeyVerification(
  flowToken: string,
  payload: Record<string, unknown>,
  signal?: AbortSignal
): Promise<SecurityProof> {
  return authResult(
    api.post(
      '/api/user/passkey/verify/finish',
      { flow_token: flowToken, credential: payload },
      { ...authRequestOptions, singleUseAuthorization: true, signal }
    )
  )
}
