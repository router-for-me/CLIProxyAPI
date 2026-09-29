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
import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { useSecureVerification } from '@/features/auth/secure-verification'
import { handleServerError } from '@/lib/handle-server-error'
import { AuthOperationError } from '@/lib/secure-verification'

import { enable2FA, setup2FA, type TwoFASetupData } from '../api'

type SetupState =
  | { phase: 'closed' | 'initializing' }
  | { phase: 'authorizing' }
  | { phase: 'ready' | 'enabling'; setup: TwoFASetupData; error?: string }

interface SetupFlow {
  controller: AbortController
  setup?: TwoFASetupData
  submitting: boolean
}

export function useTwoFASetup(refreshStatus: () => void | Promise<void>) {
  const { t } = useTranslation()
  const verification = useSecureVerification()
  const requestVerification = verification.requestVerification
  const cancelVerification = verification.cancel
  const [state, setState] = useState<SetupState>({ phase: 'closed' })
  const currentFlow = useRef<SetupFlow | null>(null)

  const cancel = useCallback(() => {
    const current = currentFlow.current
    currentFlow.current = null
    current?.controller.abort()
    cancelVerification()
    setState({ phase: 'closed' })
  }, [cancelVerification])
  useEffect(() => cancel, [cancel])

  const initialize = useCallback(
    async (current: SetupFlow) => {
      current.setup = undefined
      setState({ phase: 'authorizing' })
      const proof = await requestVerification({ scope: '2fa.setup' })
      if (currentFlow.current !== current) return
      if (!proof) {
        cancel()
        return
      }
      setState({ phase: 'initializing' })
      try {
        const setup = await setup2FA(
          proof.proof_token,
          current.controller.signal
        )
        if (currentFlow.current !== current) return
        current.setup = setup
        setState({ phase: 'ready', setup })
      } catch (error) {
        if (currentFlow.current !== current) return
        handleServerError(AuthOperationError.from(error))
        cancel()
        await refreshStatus()
      }
    },
    [cancel, refreshStatus, requestVerification]
  )

  const start = useCallback(() => {
    if (currentFlow.current) return
    const current: SetupFlow = {
      controller: new AbortController(),
      submitting: false,
    }
    currentFlow.current = current
    void initialize(current)
  }, [initialize])

  const enable = useCallback(
    async (code: string) => {
      const current = currentFlow.current
      if (!current?.setup || current.submitting || state.phase !== 'ready') {
        return
      }
      current.submitting = true
      const setup = current.setup
      setState({ phase: 'enabling', setup })
      try {
        if (setup.expires_at * 1000 <= Date.now()) {
          toast.info(
            t(
              'Setup expired. Scan the new QR code and save the new backup codes.'
            )
          )
          await initialize(current)
          return
        }
        await enable2FA(code, setup.flow_token, current.controller.signal)
        if (currentFlow.current !== current) return
        cancel()
        toast.success(t('Two-factor authentication enabled successfully!'))
        await refreshStatus()
      } catch (error) {
        if (currentFlow.current !== current) return
        const failure = AuthOperationError.from(error)
        if (failure.code === 'TWOFA_SETUP_INVALID') {
          toast.info(
            t(
              'Setup expired. Scan the new QR code and save the new backup codes.'
            )
          )
          await initialize(current)
          return
        }
        if (failure.code === 'TWOFA_CODE_INVALID') {
          setState({ phase: 'ready', setup, error: failure.message })
          return
        }
        cancel()
        handleServerError(failure)
        toast.info(t('Verify again before retrying this action.'))
        await refreshStatus()
      } finally {
        current.submitting = false
      }
    },
    [cancel, initialize, refreshStatus, state.phase, t]
  )

  const setup = 'setup' in state ? (state.setup ?? null) : null
  return {
    start,
    active: state.phase !== 'closed',
    verificationDialogProps: verification.dialogProps,
    setupDialogProps: {
      open:
        state.phase === 'initializing' ||
        state.phase === 'ready' ||
        state.phase === 'enabling',
      setupData: setup,
      loading: state.phase === 'enabling',
      initializing: state.phase === 'initializing',
      error: 'error' in state ? state.error : undefined,
      onCancel: cancel,
      onEnable: enable,
    },
  }
}
