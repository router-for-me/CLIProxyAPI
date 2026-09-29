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
import { api, isAuthBundle } from '@/lib/api'
import { buildOAuthAuthorizationUrl } from '@/lib/oauth'
import { isPasskeySupported } from '@/lib/passkey'
import {
  AuthOperationError,
  authRequestOptions,
  authResult,
} from '@/lib/secure-verification'
import type { AuthBundle } from '@/stores/auth-store'

import { createOAuthAuthorization } from '../api'
import { openOAuthPopup } from '../lib/oauth-popup'
import { encryptPassword } from '../lib/password-encryption'
import {
  beginPasskeyVerification,
  finishPasskeyVerification,
} from '../passkey/api'
import {
  requestPasskeyAssertion,
  rememberPasskeyRPID,
  type PasskeyDomains,
} from '../passkey/assertion'
import type { PasskeyOptionsPayload } from '../passkey/types'
import type { SystemStatus } from '../types'
import type {
  SecurityProof,
  LoginChallenge,
  SecurityProofScope,
  VerificationInput,
  VerificationOperation,
  VerificationRequirements,
} from './types'

export async function checkVerificationMethods(
  scope: SecurityProofScope,
  signal?: AbortSignal
): Promise<VerificationRequirements> {
  const [requirements, passkeySupported] = await Promise.all([
    authResult<VerificationRequirements>(
      api.get('/api/verify/methods', {
        ...authRequestOptions,
        params: { scope },
        signal,
        disableDuplicate: true,
      })
    ),
    isPasskeySupported(),
  ])
  return withDeviceAvailability(requirements, passkeySupported)
}

function withDeviceAvailability(
  requirements: VerificationRequirements,
  passkeySupported: boolean
): VerificationRequirements {
  return {
    ...requirements,
    methods: requirements.methods.map((option) => {
      if (
        option.method === 'passkey' &&
        option.available &&
        !passkeySupported
      ) {
        return {
          ...option,
          available: false,
          reason: 'This device does not support Passkey verification.',
        }
      }
      return option
    }),
  }
}

export function isLoginChallenge(value: unknown): value is LoginChallenge {
  if (!value || typeof value !== 'object') return false
  const challenge = value as Partial<LoginChallenge>
  return (
    challenge.require_verification === true &&
    typeof challenge.flow_token === 'string' &&
    challenge.flow_token.length > 0 &&
    typeof challenge.expires_at === 'number' &&
    Number.isFinite(challenge.expires_at) &&
    Array.isArray(challenge.methods) &&
    challenge.methods.length > 0 &&
    challenge.methods.every(
      (option) =>
        option &&
        (option.method === '2fa' || option.method === 'passkey') &&
        typeof option.available === 'boolean'
    )
  )
}

export async function getLoginVerificationRequirements(
  challenge: LoginChallenge,
  signal: AbortSignal
): Promise<VerificationRequirements> {
  if (challenge.expires_at * 1000 <= Date.now()) {
    throw new AuthOperationError(
      'Login flow expired. Please sign in again.',
      'AUTH_FLOW_INVALID'
    )
  }
  const supported = await isPasskeySupported()
  signal.throwIfAborted()
  return withDeviceAvailability(
    {
      scope: 'auth.login',
      methods: challenge.methods,
      oauth_providers: [],
      password_encryption_enabled: false,
    },
    supported
  )
}

export async function verifyLogin(
  input: VerificationInput,
  challenge: LoginChallenge,
  signal: AbortSignal,
  onDomains?: (domains: PasskeyDomains) => void
): Promise<AuthBundle> {
  if (challenge.expires_at * 1000 <= Date.now()) {
    throw new AuthOperationError(
      'Login flow expired. Please sign in again.',
      'AUTH_FLOW_INVALID'
    )
  }
  const options = { ...authRequestOptions, skipAuthRefresh: true, signal }
  let result: unknown
  if (input.method === '2fa') {
    result = await authResult(
      api.post(
        '/api/user/login/verify',
        {
          flow_token: challenge.flow_token,
          method: '2fa',
          code: input.code.trim(),
        },
        options
      )
    )
  } else if (input.method === 'passkey') {
    const passkey = await requestPasskeyAssertion(
      (rpID) =>
        authResult<PasskeyOptionsPayload>(
          api.post(
            '/api/user/login/passkey/begin',
            {
              flow_token: challenge.flow_token,
              ...(rpID ? { rp_id: rpID } : {}),
            },
            options
          )
        ),
      signal,
      { rpID: input.rpID, onDomains }
    )
    result = await authResult(
      api.post(
        '/api/user/login/passkey/finish',
        {
          flow_token: challenge.flow_token,
          passkey_flow_token: passkey.flowToken,
          credential: passkey.assertion,
        },
        options
      )
    )
    signal.throwIfAborted()
    if (!isAuthBundle(result)) throw new AuthOperationError('Login failed')
    rememberPasskeyRPID(passkey.rpID)
  } else {
    throw new AuthOperationError(
      'This verification method is not allowed for this action.'
    )
  }
  signal.throwIfAborted()
  if (!isAuthBundle(result)) throw new AuthOperationError('Login failed')
  return result
}

export async function verify(
  input: VerificationInput,
  operation: VerificationOperation,
  passwordEncryptionEnabled: boolean,
  signal: AbortSignal,
  onDomains?: (domains: PasskeyDomains) => void
): Promise<SecurityProof> {
  try {
    const operationFields = {
      scope: operation.scope,
      ...(operation.context ? { context: operation.context } : {}),
    }
    let proof: SecurityProof
    switch (input.method) {
      case 'session':
        proof = await authResult<SecurityProof>(
          api.post(
            '/api/verify',
            { method: 'session', ...operationFields },
            {
              ...authRequestOptions,
              signal,
            }
          )
        )
        break
      case '2fa':
        proof = await authResult<SecurityProof>(
          api.post(
            '/api/verify',
            {
              method: input.method,
              ...operationFields,
              code: input.code.trim(),
            },
            { ...authRequestOptions, signal }
          )
        )
        break
      case 'password': {
        const passwordFields = passwordEncryptionEnabled
          ? await encryptPassword(input.password)
          : { password: input.password }
        signal.throwIfAborted()
        proof = await authResult<SecurityProof>(
          api.post(
            '/api/verify',
            { method: input.method, ...operationFields, ...passwordFields },
            { ...authRequestOptions, signal }
          )
        )
        break
      }
      case 'passkey':
        proof = await verifyPasskey(operation, signal, input.rpID, onDomains)
        break
      case 'oauth':
        proof = await verifyOAuth(input.provider, operation, signal)
        break
    }
    signal.throwIfAborted()
    if (
      !proof.proof_token ||
      proof.scope !== operation.scope ||
      proof.method !== input.method ||
      proof.expires_at * 1000 <= Date.now()
    ) {
      throw new AuthOperationError('Verification proof was not returned')
    }
    return proof
  } catch (error) {
    throw AuthOperationError.from(error)
  }
}

async function verifyPasskey(
  operation: VerificationOperation,
  signal: AbortSignal,
  rpID?: string,
  onDomains?: (domains: PasskeyDomains) => void
): Promise<SecurityProof> {
  const passkey = await requestPasskeyAssertion(
    (selected) => beginPasskeyVerification(operation, signal, selected),
    signal,
    { rpID, onDomains }
  )
  const proof = await finishPasskeyVerification(
    passkey.flowToken,
    passkey.assertion,
    signal
  )
  signal.throwIfAborted()
  if (
    proof.proof_token &&
    proof.method === 'passkey' &&
    proof.scope === operation.scope &&
    proof.expires_at * 1000 > Date.now()
  ) {
    rememberPasskeyRPID(passkey.rpID)
  }
  return proof
}

async function verifyOAuth(
  provider: string,
  operation: VerificationOperation,
  signal: AbortSignal
): Promise<SecurityProof> {
  const exchange = await openOAuthPopup({
    provider,
    intent: 'verify',
    signal,
    prepare: async (popupSignal) => {
      const [authorization, status] = await Promise.all([
        createOAuthAuthorization(provider, 'verify', operation, popupSignal),
        authResult<SystemStatus>(
          api.get('/api/status', {
            ...authRequestOptions,
            signal: popupSignal,
            disableDuplicate: true,
          })
        ),
      ])
      return {
        state: authorization.state,
        url:
          authorization.authorizationUrl ??
          buildOAuthAuthorizationUrl(provider, authorization.state, status),
      }
    },
  })
  try {
    const callback = exchange.callback
    const proof = await authResult<SecurityProof>(
      api.get(`/api/oauth/${provider}`, {
        ...authRequestOptions,
        singleUseAuthorization: true,
        disableDuplicate: true,
        signal: exchange.signal,
        params: {
          state: callback.state,
          code: callback.code,
          error: callback.error,
          error_description: callback.errorDescription,
        },
      })
    )
    exchange.signal.throwIfAborted()
    exchange.finish({ success: true })
    return proof
  } catch (error) {
    const failure = AuthOperationError.from(
      exchange.signal.aborted ? exchange.signal.reason : error
    )
    exchange.finish({ success: false, message: failure.message })
    throw failure
  }
}
