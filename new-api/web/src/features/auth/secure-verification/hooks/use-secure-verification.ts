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
import { useCallback, useEffect, useReducer, useRef, useState } from 'react'

import { AuthOperationError } from '@/lib/secure-verification'
import type { AuthBundle } from '@/stores/auth-store'

import type { PasskeyDomains } from '../../passkey/assertion'
import {
  checkVerificationMethods,
  getLoginVerificationRequirements,
  verify,
  verifyLogin,
} from '../api'
import type {
  RequestVerificationOptions,
  RequestLoginVerificationOptions,
  VerificationRequest,
  LoginChallenge,
  SecureVerificationState,
  SecurityProof,
  VerificationInput,
  VerificationRequirements,
} from '../types'

type VerificationAction =
  | { type: 'reset' }
  | { type: 'loading'; request: VerificationRequest }
  | { type: 'loaded'; requirements: VerificationRequirements }
  | { type: 'input'; input: VerificationInput }
  | { type: 'submit' }
  | { type: 'error'; error: string }

function verificationReducer(
  state: SecureVerificationState,
  action: VerificationAction
): SecureVerificationState {
  switch (action.type) {
    case 'reset':
      return { phase: 'idle' }
    case 'loading':
      return { phase: 'loading', request: action.request }
    case 'loaded': {
      if (state.phase !== 'loading') return state
      const methods = action.requirements.methods.filter(
        (option) => option.available
      )
      const preferred =
        methods.find((option) => option.method === 'passkey') ?? methods[0]
      let input: VerificationInput | null = null
      switch (preferred?.method) {
        case '2fa':
          input = { method: '2fa', code: '' }
          break
        case 'password':
          input = { method: 'password', password: '' }
          break
        case 'passkey':
          input = { method: 'passkey' }
          break
        case 'oauth':
          input = {
            method: 'oauth',
            provider: action.requirements.oauth_providers[0]?.slug ?? '',
          }
          break
      }
      return {
        phase: 'ready',
        request: state.request,
        requirements: action.requirements,
        input,
      }
    }
    case 'input':
      if (
        state.phase !== 'ready' ||
        !state.requirements.methods.some(
          (option) => option.method === action.input.method && option.available
        )
      ) {
        return state
      }
      return { ...state, input: action.input, error: undefined }
    case 'submit': {
      if (state.phase !== 'ready') return state
      let input = state.input
      if (input?.method === 'password') {
        input = { method: 'password', password: '' }
      }
      if (input?.method === '2fa') input = { method: '2fa', code: '' }
      return { ...state, phase: 'verifying', input, error: undefined }
    }
    case 'error':
      if (state.phase === 'idle') return state
      if (state.phase === 'loading' || state.phase === 'error') {
        return { phase: 'error', request: state.request, error: action.error }
      }
      return { ...state, phase: 'ready', error: action.error }
  }
}

interface PendingVerificationBase {
  controller: AbortController
  reject: (error: unknown) => void
  submitting: boolean
}

type PendingVerification = PendingVerificationBase &
  (
    | {
        kind: 'operation'
        request: RequestVerificationOptions
        resolve: (proof: SecurityProof | null) => void
        initialPassword?: string
      }
    | {
        kind: 'login'
        request: RequestLoginVerificationOptions
        resolve: (bundle: AuthBundle | null) => void
        initialPassword?: never
      }
  )

export function useSecureVerification() {
  const [state, dispatch] = useReducer(verificationReducer, { phase: 'idle' })
  const pending = useRef<PendingVerification | null>(null)
  const [passkeyDomains, setPasskeyDomains] = useState<PasskeyDomains | null>(
    null
  )

  const cancel = useCallback(() => {
    const current = pending.current
    pending.current = null
    current?.controller.abort()
    if (current) current.initialPassword = undefined
    current?.resolve(null)
    setPasskeyDomains(null)
    dispatch({ type: 'reset' })
  }, [])

  useEffect(() => cancel, [cancel])

  const loadRequirements = useCallback(async (current: PendingVerification) => {
    dispatch({ type: 'loading', request: current.request })
    try {
      const requirements =
        current.kind === 'login'
          ? await getLoginVerificationRequirements(
              current.request.challenge,
              current.controller.signal
            )
          : await checkVerificationMethods(
              current.request.scope,
              current.controller.signal
            )
      if (pending.current !== current) return
      const initialPassword = current.initialPassword
      current.initialPassword = undefined
      if (
        current.kind === 'operation' &&
        initialPassword !== undefined &&
        requirements.methods.length === 1 &&
        requirements.methods[0].method === 'password' &&
        requirements.methods[0].available
      ) {
        try {
          const proof = await verify(
            { method: 'password', password: initialPassword },
            current.request,
            requirements.password_encryption_enabled,
            current.controller.signal
          )
          if (pending.current !== current) return
          pending.current = null
          dispatch({ type: 'reset' })
          current.resolve(proof)
        } catch (error) {
          if (pending.current !== current) return
          pending.current = null
          dispatch({ type: 'reset' })
          current.reject(error)
        }
        return
      }
      if (
        current.kind === 'operation' &&
        requirements.methods.length === 1 &&
        requirements.methods[0].method === 'session' &&
        requirements.methods[0].available
      ) {
        const proof = await verify(
          { method: 'session' },
          current.request,
          requirements.password_encryption_enabled,
          current.controller.signal
        )
        if (pending.current !== current) return
        pending.current = null
        dispatch({ type: 'reset' })
        current.resolve(proof)
        return
      }
      if (pending.current === current) {
        dispatch({ type: 'loaded', requirements })
      }
    } catch (error) {
      if (pending.current === current) {
        dispatch({
          type: 'error',
          error: AuthOperationError.from(error).message,
        })
      }
    }
  }, [])

  const requestVerification = useCallback(
    (
      request: RequestVerificationOptions,
      initialPassword?: string
    ): Promise<SecurityProof | null> => {
      if (pending.current) return Promise.resolve(null)
      return new Promise((resolve, reject) => {
        const current: PendingVerification = {
          kind: 'operation',
          request: structuredClone(request),
          resolve,
          reject,
          initialPassword,
          controller: new AbortController(),
          submitting: false,
        }
        pending.current = current
        setPasskeyDomains(null)
        void loadRequirements(current)
      })
    },
    [loadRequirements]
  )

  const requestLoginVerification = useCallback(
    (challenge: LoginChallenge): Promise<AuthBundle | null> => {
      if (pending.current) return Promise.resolve(null)
      return new Promise((resolve, reject) => {
        const current: PendingVerification = {
          kind: 'login',
          request: {
            scope: 'auth.login',
            challenge: structuredClone(challenge),
          },
          resolve,
          reject,
          controller: new AbortController(),
          submitting: false,
        }
        pending.current = current
        setPasskeyDomains(null)
        void loadRequirements(current)
      })
    },
    [loadRequirements]
  )

  const executeVerification = useCallback(
    async (override?: VerificationInput) => {
      const current = pending.current
      if (!current || current.submitting || state.phase !== 'ready') return
      const input = override ?? state.input
      if (!input) return
      const available = state.requirements.methods.some(
        (option) => option.method === input.method && option.available
      )
      if (!available) return
      current.submitting = true
      if (override) dispatch({ type: 'input', input })
      dispatch({ type: 'submit' })
      try {
        if (current.kind === 'login') {
          const bundle = await verifyLogin(
            input,
            current.request.challenge,
            current.controller.signal,
            (domains) => {
              if (pending.current === current) setPasskeyDomains(domains)
            }
          )
          if (pending.current !== current) return
          current.resolve(bundle)
        } else {
          const proof = await verify(
            input,
            current.request,
            state.requirements.password_encryption_enabled,
            current.controller.signal,
            (domains) => {
              if (pending.current === current) setPasskeyDomains(domains)
            }
          )
          if (pending.current !== current) return
          current.resolve(proof)
        }
        pending.current = null
        dispatch({ type: 'reset' })
      } catch (error) {
        if (pending.current !== current) return
        const failure = AuthOperationError.from(error)
        if (failure.code === 'AUTH_CANCELLED') {
          cancel()
          return
        }
        dispatch({ type: 'error', error: failure.message })
      } finally {
        current.submitting = false
      }
    },
    [cancel, state]
  )

  const retry = useCallback(() => {
    if (pending.current && state.phase === 'error') {
      void loadRequirements(pending.current)
    }
  }, [loadRequirements, state.phase])
  const setInput = useCallback(
    (input: VerificationInput) => dispatch({ type: 'input', input }),
    []
  )

  return {
    requestVerification,
    requestLoginVerification,
    cancel,
    isActive: state.phase !== 'idle',
    dialogProps: {
      state,
      passkeyDomains,
      onCancel: cancel,
      onRetry: retry,
      onInputChange: setInput,
      onVerify: executeVerification,
    },
  }
}
