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
import { Mail, Shield, Send, Link2, Unlink } from 'lucide-react'
import { useCallback, useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { SiGithub, SiWechat, SiLinux } from 'react-icons/si'
import { toast } from 'sonner'

import { IconDiscord } from '@/assets/brand-icons'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { createOAuthAuthorization } from '@/features/auth/api'
import {
  openOAuthPopup,
  type OAuthPopupExchange,
} from '@/features/auth/lib/oauth-popup'
import { SecureVerificationDialog } from '@/features/auth/secure-verification'
import type { CustomOAuthProviderInfo } from '@/features/auth/types'
import { getSelfOAuthBindings, unbindCustomOAuth } from '@/features/profile/api'
import type {
  UserProfile,
  BindingItem,
  AccountSecurityResult,
} from '@/features/profile/types'
import { useDialogs } from '@/hooks/use-dialog'
import { useStatus } from '@/hooks/use-status'
import { api } from '@/lib/api'
import {
  buildOAuthAuthorizationUrl,
  indexCustomOAuthBindings,
  type CustomOAuthBinding,
} from '@/lib/oauth'
import {
  AuthOperationError,
  authRequestOptions,
  authResult,
} from '@/lib/secure-verification'

import { useAccountSecurity } from '../hooks/use-account-security'
import { EmailBindDialog } from './dialogs/email-bind-dialog'
import { WeChatBindDialog } from './dialogs/wechat-bind-dialog'

// ============================================================================
// Account Bindings Tab Component
// ============================================================================

interface AccountBindingsProps {
  profile: UserProfile | null
  onUpdate: () => void
}

type DialogKey = 'email' | 'wechat'

type PreparedOAuthBinding = AccountSecurityResult & {
  provider: string
  state: string
  url: string
}

export function AccountBindings({ profile, onUpdate }: AccountBindingsProps) {
  const { t } = useTranslation()
  const dialogs = useDialogs<DialogKey>()
  const { status, loading } = useStatus()
  const [customBindings, setCustomBindings] = useState<CustomOAuthBinding[]>([])
  const [unbindTarget, setUnbindTarget] = useState<CustomOAuthBinding | null>(
    null
  )
  const security = useAccountSecurity()
  const unbinding = security.pending
  const [preparedBinding, setPreparedBinding] =
    useState<PreparedOAuthBinding | null>(null)
  const bindingsLocked =
    security.pending || Boolean(preparedBinding) || dialogs.hasAnyOpen

  const customProviders = status?.custom_oauth_providers as
    | CustomOAuthProviderInfo[]
    | undefined
  const customBindingsByProviderId = useMemo(
    () => indexCustomOAuthBindings(customBindings),
    [customBindings]
  )

  const fetchCustomBindings = useCallback(async () => {
    if (!customProviders || customProviders.length === 0) return
    try {
      const res = await getSelfOAuthBindings()
      if (res.success && res.data) {
        setCustomBindings(res.data)
      }
    } catch {
      // ignore
    }
  }, [customProviders])

  useEffect(() => {
    fetchCustomBindings()
  }, [fetchCustomBindings])

  const handleUnbindCustom = async () => {
    if (!unbindTarget) return
    const target = unbindTarget
    setUnbindTarget(null)
    const result = await security.run(async (signal) => {
      const proof = await security.verify(
        {
          scope: 'account.binding.unbind',
          context: { provider_id: target.provider_id },
        },
        signal
      )
      return unbindCustomOAuth(target.provider_id, proof, signal)
    })
    if (result) {
      toast.success(
        t('Unbound {{provider}}', { provider: target.provider_name })
      )
      await fetchCustomBindings()
      onUpdate()
    }
  }

  const startOAuthBinding = async (provider: string) => {
    const prepared = await security.run(async (signal) => {
      const proof = await security.verify(
        { scope: 'account.binding.bind', context: { provider } },
        signal
      )
      const authorization = await createOAuthAuthorization(
        provider,
        'bind',
        undefined,
        signal,
        proof
      )
      return {
        provider,
        state: authorization.state,
        url:
          authorization.authorizationUrl ??
          buildOAuthAuthorizationUrl(
            provider,
            authorization.state,
            status ?? {}
          ),
        notification_warning: false,
      }
    })
    if (prepared) setPreparedBinding(prepared)
  }

  // A separate user click opens the provider popup. Opening it after an async
  // verification response would otherwise be blocked by browsers such as Safari.
  const completeOAuthBinding = async () => {
    if (!preparedBinding) return
    const prepared = preparedBinding
    setPreparedBinding(null)
    const result = await security.run(async (signal) => {
      let exchange: OAuthPopupExchange | undefined
      try {
        exchange = await openOAuthPopup({
          provider: prepared.provider,
          intent: 'bind',
          signal,
          prepare: async () => ({ state: prepared.state, url: prepared.url }),
        })
        const callback = exchange.callback
        const outcome = await authResult<AccountSecurityResult>(
          api.get(`/api/oauth/${prepared.provider}`, {
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
        return outcome
      } catch (error) {
        const failure = AuthOperationError.from(
          exchange?.signal.aborted ? exchange.signal.reason : error
        )
        exchange?.finish({ success: false, message: failure.message })
        throw failure
      }
    })
    if (result) {
      toast.success(t('Binding successful!'))
      onUpdate()
      await fetchCustomBindings()
    }
  }

  const handleBindCustomOAuth = (provider: CustomOAuthProviderInfo) =>
    startOAuthBinding(provider.slug)

  const closeDialogs = dialogs.closeAll
  useEffect(() => {
    setPreparedBinding(null)
    setUnbindTarget(null)
    closeDialogs()
  }, [security.sessionKey, closeDialogs])

  if (!profile || !status || loading) return null

  const bindings: BindingItem[] = [
    {
      id: 'email',
      label: t('Email'),
      icon: Mail,
      value: profile.email,
      isBound: Boolean(profile.email),
      isEnabled: true,
      onBind: () => dialogs.open('email'),
    },
    {
      id: 'wechat',
      label: t('WeChat'),
      icon: SiWechat as React.ComponentType<{ className?: string }>,
      value: undefined,
      isBound: Boolean(
        (profile as unknown as Record<string, unknown>).wechat_id
      ),
      isEnabled: status?.wechat_login || false,
      onBind: () => dialogs.open('wechat'),
    },
    {
      id: 'github',
      label: t('GitHub'),
      icon: SiGithub,
      value: (profile as unknown as Record<string, unknown>).github_id as
        | string
        | undefined,
      isBound: Boolean(
        (profile as unknown as Record<string, unknown>).github_id
      ),
      isEnabled: status?.github_oauth || false,
      onBind: () => void startOAuthBinding('github'),
    },
    {
      id: 'discord',
      label: t('Discord'),
      icon: IconDiscord,
      value: (profile as unknown as Record<string, unknown>).discord_id as
        | string
        | undefined,
      isBound: Boolean(
        (profile as unknown as Record<string, unknown>).discord_id
      ),
      isEnabled: status?.discord_oauth || false,
      onBind: () => void startOAuthBinding('discord'),
    },
    {
      id: 'oidc',
      label: t('OIDC'),
      icon: Shield,
      value: (profile as unknown as Record<string, unknown>).oidc_id as
        | string
        | undefined,
      isBound: Boolean((profile as unknown as Record<string, unknown>).oidc_id),
      isEnabled: status?.oidc_enabled || false,
      onBind: () => void startOAuthBinding('oidc'),
    },
    {
      id: 'telegram',
      label: t('Telegram'),
      icon: Send,
      value: (profile as unknown as Record<string, unknown>).telegram_id as
        | string
        | undefined,
      isBound: Boolean(
        (profile as unknown as Record<string, unknown>).telegram_id
      ),
      isEnabled: status?.telegram_oauth || false,
      onBind: () => void startOAuthBinding('telegram'),
    },
    {
      id: 'linuxdo',
      label: t('LinuxDO'),
      icon: SiLinux as React.ComponentType<{ className?: string }>,
      value: (profile as unknown as Record<string, unknown>).linux_do_id as
        | string
        | undefined,
      isBound: Boolean(
        (profile as unknown as Record<string, unknown>).linux_do_id
      ),
      isEnabled: status?.linuxdo_oauth || false,
      onBind: () => void startOAuthBinding('linuxdo'),
    },
  ].filter((binding) => binding.isEnabled)

  return (
    <>
      <ul
        aria-label={t('Account Bindings')}
        className='grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3'
      >
        {bindings.map((binding) => {
          let actionLabel = t('Bind')
          if (binding.isBound && binding.id === 'email') {
            actionLabel = t('Change')
          } else if (binding.isBound) {
            actionLabel = t('Bound')
          }

          return (
            <li
              key={binding.id}
              className='flex min-w-0 items-center justify-between gap-2 rounded-lg border px-2.5 py-2'
            >
              <div className='flex min-w-0 items-center gap-2'>
                <div className='bg-muted shrink-0 rounded-md p-1.5'>
                  <binding.icon className='h-4 w-4' />
                </div>
                <div className='min-w-0'>
                  <div className='flex items-center gap-1.5'>
                    <p
                      className='truncate text-sm font-medium'
                      title={binding.label}
                    >
                      {binding.label}
                    </p>
                    {binding.isBound && (
                      <StatusBadge
                        label={t('Bound')}
                        variant='success'
                        copyable={false}
                      />
                    )}
                  </div>
                  <p className='text-muted-foreground truncate text-xs'>
                    {binding.value || t('Not bound')}
                  </p>
                </div>
              </div>
              <Button
                variant='outline'
                size='sm'
                className='h-7 shrink-0 px-2.5 text-xs'
                onClick={binding.onBind}
                disabled={
                  bindingsLocked || (binding.isBound && binding.id !== 'email')
                }
              >
                {actionLabel}
              </Button>
            </li>
          )
        })}
        {customProviders?.map((provider) => {
          const binding = customBindingsByProviderId.get(provider.id)
          const isBound = !!binding
          return (
            <li
              key={provider.id}
              className='flex min-w-0 items-center justify-between gap-2 rounded-lg border px-2.5 py-2'
            >
              <div className='flex min-w-0 items-center gap-2'>
                <div className='bg-muted shrink-0 rounded-md p-1.5'>
                  <Link2 className='h-4 w-4' />
                </div>
                <div className='min-w-0'>
                  <div className='flex items-center gap-1.5'>
                    <p
                      className='truncate text-sm font-medium'
                      title={provider.name}
                    >
                      {provider.name}
                    </p>
                    {isBound && (
                      <StatusBadge
                        label={t('Bound')}
                        variant='success'
                        copyable={false}
                      />
                    )}
                  </div>
                  <p className='text-muted-foreground truncate text-xs'>
                    {isBound
                      ? binding?.provider_user_id || t('Bound')
                      : t('Not bound')}
                  </p>
                </div>
              </div>
              {isBound ? (
                <Button
                  variant='ghost'
                  size='sm'
                  className='text-destructive h-7 shrink-0 px-2.5 text-xs'
                  onClick={() => setUnbindTarget(binding)}
                  disabled={bindingsLocked}
                >
                  <Unlink className='mr-1 h-3 w-3' />
                  {t('Unbind')}
                </Button>
              ) : (
                <Button
                  variant='outline'
                  size='sm'
                  className='h-7 shrink-0 px-2.5 text-xs'
                  onClick={() => void handleBindCustomOAuth(provider)}
                  disabled={bindingsLocked}
                >
                  {t('Bind')}
                </Button>
              )}
            </li>
          )
        })}
      </ul>

      {security.showVerification && (
        <SecureVerificationDialog {...security.verificationDialogProps} />
      )}
      <ConfirmDialog
        open={preparedBinding !== null}
        onOpenChange={(open) => {
          if (!open) setPreparedBinding(null)
        }}
        title={t('Continue account binding')}
        desc={t(
          'Your identity has been verified. Continue to the provider to finish linking your account.'
        )}
        handleConfirm={() => void completeOAuthBinding()}
        confirmText={t('Continue')}
      />
      {/* Custom OAuth Unbind Confirmation */}
      <ConfirmDialog
        open={!!unbindTarget}
        onOpenChange={(open) => !open && setUnbindTarget(null)}
        title={t('Confirm Unbind')}
        desc={t(
          'Are you sure you want to unbind {{provider}}? You will no longer be able to log in via this method.',
          {
            provider: unbindTarget?.provider_name || '',
          }
        )}
        confirmText={t('Confirm Unbind')}
        destructive
        handleConfirm={handleUnbindCustom}
        isLoading={unbinding}
      />

      {/* Email Bind Dialog */}
      <EmailBindDialog
        open={dialogs.isOpen('email')}
        onOpenChange={(open) =>
          open ? dialogs.open('email') : dialogs.close('email')
        }
        currentEmail={profile.email}
        onSuccess={onUpdate}
      />

      {/* WeChat Bind Dialog */}
      <WeChatBindDialog
        open={dialogs.isOpen('wechat')}
        qrCodeUrl={
          typeof status?.wechat_qrcode === 'string' ? status.wechat_qrcode : ''
        }
        onOpenChange={(open) =>
          open ? dialogs.open('wechat') : dialogs.close('wechat')
        }
        onSuccess={onUpdate}
      />
    </>
  )
}
