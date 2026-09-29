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
import { Separator } from '@/components/ui/separator'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'

import { estimatePrice, extendDeployment, getDeployment } from '../../api'
import { deploymentsQueryKeys } from '../../lib'

function toInt(value: unknown, fallback: number) {
  const n = typeof value === 'number' ? value : Number(value)
  return Number.isFinite(n) ? Math.max(0, Math.round(n)) : fallback
}

export function ExtendDeploymentDialog({
  open,
  onOpenChange,
  deploymentId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  deploymentId: string | number | null
}) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [hours, setHours] = useState(1)
  const [isSubmitting, setIsSubmitting] = useState(false)

  useEffect(() => {
    if (open) setHours(1)
  }, [open])

  const { data: detailsRes, isLoading: isLoadingDetails } = useQuery({
    queryKey: ['deployment-details-for-extend', deploymentId],
    queryFn: async () =>
      requireServerSuccess(
        await (deploymentId ? getDeployment(deploymentId) : null)
      ),
    enabled: open && deploymentId !== null,
  })

  const details = detailsRes?.data

  const priceParams = useMemo(() => {
    if (!details) return null
    const hardwareId = toInt(details.hardware_id, 0)
    const gpusPerContainer = toInt(details.gpus_per_container, 0)
    const replicaCount = toInt(details.total_containers, 0)
    const locations = Array.isArray(details.locations) ? details.locations : []
    const locationIds = locations
      .map((x) => {
        if (!x || typeof x !== 'object') return 0
        return toInt((x as Record<string, unknown>)?.id, 0)
      })
      .filter((x) => x > 0)

    if (
      hardwareId <= 0 ||
      gpusPerContainer <= 0 ||
      replicaCount <= 0 ||
      locationIds.length === 0
    ) {
      return null
    }

    return {
      hardware_id: hardwareId,
      gpus_per_container: gpusPerContainer,
      replica_count: replicaCount,
      location_ids: locationIds,
    }
  }, [details])

  const {
    data: priceRes,
    isLoading: isLoadingPrice,
    isFetching: isFetchingPrice,
  } = useQuery({
    queryKey: ['deployment-extend-price', deploymentId, hours, priceParams],
    queryFn: async () =>
      requireServerSuccess(
        await (priceParams
          ? estimatePrice({
              location_ids: priceParams.location_ids,
              hardware_id: priceParams.hardware_id,
              gpus_per_container: priceParams.gpus_per_container,
              replica_count: priceParams.replica_count,
              duration_hours: hours,
              currency: 'usdc',
            })
          : null)
      ),
    enabled: open && Boolean(priceParams) && hours > 0,
  })

  const priceSummary = useMemo(() => {
    const data = priceRes?.data
    if (!data || typeof data !== 'object') return ''
    const record = data as Record<string, unknown>
    const breakdown = record.price_breakdown
    let total: unknown = record.total_cost
    if (
      breakdown &&
      typeof breakdown === 'object' &&
      !Array.isArray(breakdown)
    ) {
      const b = breakdown as Record<string, unknown>
      total = b.total_cost ?? b.totalCost ?? b.TotalCost ?? total
    }
    const currency = record.currency ?? 'USDC'
    if (total === undefined || total === null) return ''
    return `${String(total)} ${String(currency).toUpperCase()}`.trim()
  }, [priceRes])

  const canSubmit = Boolean(deploymentId) && hours > 0 && !isSubmitting

  const onSubmit = async () => {
    if (!deploymentId) return
    const h = toInt(hours, 1)
    if (h <= 0) {
      toast.error(t('Please enter a valid duration'))
      return
    }
    setIsSubmitting(true)
    try {
      const res = await extendDeployment(deploymentId, h)
      if (res.success) {
        toast.success(t('Extended successfully'))
        queryClient.invalidateQueries({
          queryKey: deploymentsQueryKeys.lists(),
        })
        queryClient.invalidateQueries({ queryKey: ['deployment-details'] })
        onOpenChange(false)
        return
      }
      handleServerError(res, t('Extend failed'))
    } catch (err: unknown) {
      handleServerError(err, t('Extend failed'))
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Extend deployment')}
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
            {t('Extend')}
          </Button>
        </>
      }
    >
      {isLoadingDetails ? (
        <div className='flex items-center justify-center py-10'>
          <Loader2 className='text-muted-foreground h-6 w-6 animate-spin' />
        </div>
      ) : (
        <div className='space-y-4'>
          <div className='text-muted-foreground text-sm'>
            {t('Deployment ID')}:{' '}
            <span className='font-mono'>{deploymentId}</span>
          </div>

          <div className='space-y-2'>
            <div className='text-sm font-medium'>{t('Duration (hours)')}</div>
            <Input
              type='number'
              min={1}
              value={hours}
              onChange={(e) => setHours(toInt(e.target.value, 1))}
            />
            <div className='text-muted-foreground text-xs'>
              {t('This will extend the deployment by the specified hours.')}
            </div>
          </div>

          <Separator />

          <div className='space-y-1'>
            <div className='text-sm font-medium'>{t('Estimated cost')}</div>
            <div className='text-muted-foreground text-sm'>
              {(isLoadingPrice || isFetchingPrice) && (
                <span className='inline-flex items-center gap-2'>
                  <Loader2 className='h-4 w-4 animate-spin' />
                  {t('Calculating...')}
                </span>
              )}
              {!(isLoadingPrice || isFetchingPrice) &&
                priceParams &&
                (priceSummary || t('Not available'))}
              {!(isLoadingPrice || isFetchingPrice) &&
                !priceParams &&
                t('Not available')}
            </div>
            {!priceParams ? (
              <div className='text-muted-foreground text-xs'>
                {t('Unable to estimate price for this deployment.')}
              </div>
            ) : null}
          </div>
        </div>
      )}
    </Dialog>
  )
}
