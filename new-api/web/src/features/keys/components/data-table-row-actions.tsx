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
import type { Row } from '@tanstack/react-table'
import {
  Trash2,
  Edit,
  Power,
  PowerOff,
  ExternalLink,
  ArrowRightLeft,
  Copy,
  Link,
  Loader2,
} from 'lucide-react'
import { useCallback, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DataTableRowActionMenu } from '@/components/data-table/core/row-action-menu'
import { Button } from '@/components/ui/button'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { useChatPresets } from '@/features/chat/hooks/use-chat-presets'
import { resolveChatUrl, type ChatPreset } from '@/features/chat/lib/chat-links'
import { sendToFluent } from '@/features/chat/lib/send-to-fluent'
import { encodeChannelConnectionInfo } from '@/lib/channel-connection-info'
import { copyToClipboard } from '@/lib/copy-to-clipboard'
import { handleServerError } from '@/lib/handle-server-error'

import { updateApiKeyStatus } from '../api'
import { API_KEY_STATUS, ERROR_MESSAGES, SUCCESS_MESSAGES } from '../constants'
import { apiKeySchema } from '../types'
import { useApiKeys } from './api-keys-provider'

function getServerAddress(): string {
  try {
    const raw = localStorage.getItem('status')
    if (raw) {
      const status = JSON.parse(raw)
      if (status.server_address) return status.server_address as string
    }
  } catch {
    /* empty */
  }
  return window.location.origin
}

type DataTableRowActionsProps<TData> = {
  row: Row<TData>
}

export function DataTableRowActions<TData>({
  row,
}: DataTableRowActionsProps<TData>) {
  const { t } = useTranslation()
  const apiKey = apiKeySchema.parse(row.original)
  const {
    setOpen,
    setCurrentRow,
    triggerRefresh,
    setResolvedKey,
    resolveRealKey,
    loadingKeys,
  } = useApiKeys()
  const isEnabled = apiKey.status === API_KEY_STATUS.ENABLED
  const { chatPresets, serverAddress } = useChatPresets()
  const [isTogglingStatus, setIsTogglingStatus] = useState(false)
  const isRealKeyLoading = Boolean(loadingKeys[apiKey.id])

  const hasChatPresets = chatPresets.length > 0
  const toggleLabel = isEnabled ? t('Disable') : t('Enable')

  const handleOpenChatPreset = useCallback(
    async (preset: ChatPreset) => {
      const realKey = await resolveRealKey(apiKey.id)
      if (!realKey) return

      if (preset.type === 'fluent') {
        const success = sendToFluent(realKey, serverAddress)
        if (success) {
          toast.success(t('Sent the API key to FluentRead.'))
        } else {
          toast.info(
            t(
              'FluentRead extension not detected. Please ensure it is installed and active.'
            )
          )
        }
        return
      }

      const resolvedUrl = resolveChatUrl({
        template: preset.url,
        apiKey: realKey,
        serverAddress,
      })

      if (!resolvedUrl) {
        toast.error(t('Invalid chat link. Please contact your administrator.'))
        return
      }

      if (typeof window === 'undefined') return

      try {
        window.open(resolvedUrl, '_blank', 'noopener')
      } catch {
        window.location.href = resolvedUrl
      }
    },
    [resolveRealKey, apiKey.id, serverAddress, t]
  )

  const handleToggleStatus = async (
    event?: React.MouseEvent<HTMLButtonElement>
  ) => {
    event?.stopPropagation()
    const newStatus = isEnabled
      ? API_KEY_STATUS.DISABLED
      : API_KEY_STATUS.ENABLED

    setIsTogglingStatus(true)
    try {
      const result = await updateApiKeyStatus(apiKey.id, newStatus)
      if (result.success) {
        const message = isEnabled
          ? t(SUCCESS_MESSAGES.API_KEY_DISABLED)
          : t(SUCCESS_MESSAGES.API_KEY_ENABLED)
        toast.success(message)
        triggerRefresh()
      } else {
        handleServerError(result, t(ERROR_MESSAGES.STATUS_UPDATE_FAILED))
      }
    } catch (error) {
      handleServerError(error, t(ERROR_MESSAGES.UNEXPECTED))
    } finally {
      setIsTogglingStatus(false)
    }
  }

  let statusIcon = <Power className='size-4' />
  if (isTogglingStatus) {
    statusIcon = <Loader2 className='size-4 animate-spin' />
  } else if (isEnabled) {
    statusIcon = <PowerOff className='size-4' />
  }

  return (
    <div className='-ml-1.5 flex items-center gap-1'>
      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              onClick={handleToggleStatus}
              disabled={isTogglingStatus}
              aria-label={toggleLabel}
              className={
                isEnabled
                  ? 'text-destructive hover:text-destructive'
                  : 'text-emerald-600 hover:text-emerald-600 dark:text-emerald-400 dark:hover:text-emerald-400'
              }
            />
          }
        >
          {statusIcon}
        </TooltipTrigger>
        <TooltipContent>{toggleLabel}</TooltipContent>
      </Tooltip>

      <Tooltip>
        <TooltipTrigger
          render={
            <Button
              variant='ghost'
              size='icon-sm'
              onClick={() => {
                setCurrentRow(apiKey)
                setOpen('update')
              }}
              aria-label={t('Edit')}
            />
          }
        >
          <Edit />
        </TooltipTrigger>
        <TooltipContent>{t('Edit')}</TooltipContent>
      </Tooltip>

      <DataTableRowActionMenu
        ariaLabel={t('Open menu')}
        contentClassName='w-[200px]'
        modal={false}
      >
        <DropdownMenuItem
          disabled={isRealKeyLoading}
          onClick={async () => {
            const realKey = await resolveRealKey(apiKey.id)
            if (!realKey) return
            const ok = await copyToClipboard(realKey)
            if (ok) toast.success(t('Copied'))
          }}
        >
          {t('Copy Key')}
          <DropdownMenuShortcut>
            <Copy size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
        <DropdownMenuItem
          disabled={isRealKeyLoading}
          onClick={async () => {
            const realKey = await resolveRealKey(apiKey.id)
            if (!realKey) return
            const connStr = encodeChannelConnectionInfo(
              realKey,
              getServerAddress()
            )
            const ok = await copyToClipboard(connStr)
            if (ok) toast.success(t('Copied'))
          }}
        >
          {t('Copy Connection Info')}
          <DropdownMenuShortcut>
            <Link size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={async () => {
            const realKey = await resolveRealKey(apiKey.id)
            if (!realKey) return
            setResolvedKey(realKey)
            setCurrentRow(apiKey)
            setOpen('cc-switch')
          }}
        >
          {t('CC Switch')}
          <DropdownMenuShortcut>
            <ArrowRightLeft size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
        {hasChatPresets && (
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>{t('Chat')}</DropdownMenuSubTrigger>
            <DropdownMenuSubContent>
              {chatPresets.map((preset) => (
                <DropdownMenuItem
                  key={preset.id}
                  onClick={() => handleOpenChatPreset(preset)}
                >
                  {preset.name}
                  {preset.type !== 'web' && (
                    <DropdownMenuShortcut>
                      <ExternalLink size={16} />
                    </DropdownMenuShortcut>
                  )}
                </DropdownMenuItem>
              ))}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        )}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          onClick={() => {
            setCurrentRow(apiKey)
            setOpen('delete')
          }}
          className='text-destructive focus:text-destructive'
        >
          {t('Delete')}
          <DropdownMenuShortcut>
            <Trash2 size={16} />
          </DropdownMenuShortcut>
        </DropdownMenuItem>
      </DataTableRowActionMenu>
    </div>
  )
}
