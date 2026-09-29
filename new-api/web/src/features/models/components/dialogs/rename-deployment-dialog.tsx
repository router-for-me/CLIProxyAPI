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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { checkClusterNameAvailability, updateDeploymentName } from '../../api'
import { deploymentsQueryKeys } from '../../lib'

export function RenameDeploymentDialog({
  open,
  onOpenChange,
  deploymentId,
  currentName,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  deploymentId: string | number | null
  currentName?: string
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [name, setName] = useState(currentName || '')
  const [isSubmitting, setIsSubmitting] = useState(false)

  useEffect(() => {
    if (open) setName(currentName || '')
  }, [open, currentName])

  const trimmed = name.trim()

  const { data: checkRes, isFetching: isChecking } = useQuery({
    queryKey: ['deployment-rename-check', trimmed],
    queryFn: async () =>
      requireServerSuccess(
        await (trimmed ? checkClusterNameAvailability(trimmed) : null)
      ),
    enabled: open && Boolean(trimmed),
    staleTime: 10_000,
  })

  const available =
    checkRes?.success === true ? checkRes?.data?.available : undefined

  const helper = useMemo(() => {
    if (!trimmed) return t('Enter a new name')
    if (isChecking) return t('Checking name...')
    if (available === true) return t('Name is available')
    if (available === false) return t('Name is not available')
    return ''
  }, [available, isChecking, t, trimmed])

  const canSubmit =
    Boolean(deploymentId) &&
    Boolean(trimmed) &&
    available !== false &&
    !isSubmitting

  const onSubmit = async () => {
    if (!deploymentId) return
    if (!trimmed) {
      toast.error(t('Please enter a name'))
      return
    }
    if (available === false) {
      toast.error(t('Name is not available'))
      return
    }

    setIsSubmitting(true)
    try {
      const res = await updateDeploymentName(deploymentId, trimmed)
      if (res.success) {
        toast.success(t('Renamed successfully'))
        queryClient.invalidateQueries({
          queryKey: deploymentsQueryKeys.lists(),
        })
        queryClient.invalidateQueries({ queryKey: ['deployment-details'] })
        onOpenChange(false)
        return
      }
      handleServerError(res, t('Rename failed'))
    } catch (err: unknown) {
      handleServerError(err, t('Rename failed'))
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Rename deployment')}
      contentClassName='sm:max-w-lg'
      footerClassName='mt-4'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button variant='outline' onClick={() => onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button onClick={() => void onSubmit()} disabled={!canSubmit}>
            {isSubmitting ? (
              <Loader2 className='mr-2 h-4 w-4 animate-spin' />
            ) : null}
            {t('Rename')}
          </Button>
        </>
      }
    >
      <div className='space-y-2'>
        <div className='text-muted-foreground text-sm'>
          {t('Deployment ID')}:{' '}
          <span className='font-mono'>{deploymentId}</span>
        </div>
        <Input
          placeholder={t('Enter a new name')}
          value={name}
          onChange={(e) => setName(e.target.value)}
          autoComplete='off'
        />
        <div className='text-muted-foreground text-xs'>{helper}</div>
      </div>
    </Dialog>
  )
}
