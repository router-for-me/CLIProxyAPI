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
import { Save, Settings2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  CONSUMPTION_DISTRIBUTION_CHART_OPTIONS,
  MODEL_ANALYTICS_CHART_OPTIONS,
  TIME_GRANULARITY_OPTIONS,
  TIME_RANGE_PRESETS,
} from '@/features/dashboard/constants'
import type {
  ConsumptionDistributionChartType,
  DashboardChartPreferences,
  ModelAnalyticsChartTab,
} from '@/features/dashboard/types'
import type { TimeGranularity } from '@/lib/time'

interface ModelsChartPreferencesProps {
  preferences: DashboardChartPreferences
  onPreferencesChange: (preferences: DashboardChartPreferences) => void
}

export function ModelsChartPreferences(props: ModelsChartPreferencesProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<DashboardChartPreferences>(
    props.preferences
  )

  const handleOpenChange = (nextOpen: boolean) => {
    if (nextOpen) setDraft(props.preferences)
    setOpen(nextOpen)
  }

  const handleSave = () => {
    props.onPreferencesChange(draft)
    setOpen(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={handleOpenChange}
      trigger={
        <Button variant='outline' size='sm'>
          <Settings2 className='mr-2 h-4 w-4' />
          {t('Preferences')}
        </Button>
      }
      title={t('Model Analytics Defaults')}
      description={t('Set default ranges and charts for model analytics.')}
      contentClassName='sm:max-w-md'
      contentHeight='auto'
      bodyClassName='grid gap-3'
      footer={
        <Button onClick={handleSave} type='button'>
          <Save className='mr-2 h-4 w-4' />
          {t('Save Preferences')}
        </Button>
      }
    >
      <div className='grid gap-1.5'>
        <Label htmlFor='default-time-range'>{t('Default range')}</Label>
        <Select
          items={[
            ...TIME_RANGE_PRESETS.map((option) => ({
              value: String(option.days),
              label: t(option.label),
            })),
          ]}
          value={String(draft.defaultTimeRangeDays)}
          onValueChange={(value) =>
            setDraft((prev) => ({
              ...prev,
              defaultTimeRangeDays: Number(value),
            }))
          }
        >
          <SelectTrigger id='default-time-range'>
            <SelectValue placeholder={t('Select default range')} />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <SelectGroup>
              {TIME_RANGE_PRESETS.map((option) => (
                <SelectItem key={option.days} value={String(option.days)}>
                  {t(option.label)}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      <div className='grid gap-1.5'>
        <Label htmlFor='default-time-granularity'>
          {t('Default time granularity')}
        </Label>
        <Select
          items={[
            ...TIME_GRANULARITY_OPTIONS.map((option) => ({
              value: option.value,
              label: t(option.label),
            })),
          ]}
          value={draft.defaultTimeGranularity}
          onValueChange={(value) =>
            setDraft((prev) => ({
              ...prev,
              defaultTimeGranularity: value as TimeGranularity,
            }))
          }
        >
          <SelectTrigger id='default-time-granularity'>
            <SelectValue placeholder={t('Select time granularity')} />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <SelectGroup>
              {TIME_GRANULARITY_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {t(option.label)}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      <div className='grid gap-1.5'>
        <Label htmlFor='consumption-distribution-chart'>
          {t('Default consumption chart')}
        </Label>
        <Select
          items={[
            ...CONSUMPTION_DISTRIBUTION_CHART_OPTIONS.map((option) => ({
              value: option.value,
              label: t(option.labelKey),
            })),
          ]}
          value={draft.consumptionDistributionChart}
          onValueChange={(value) =>
            setDraft((prev) => ({
              ...prev,
              consumptionDistributionChart:
                value as ConsumptionDistributionChartType,
            }))
          }
        >
          <SelectTrigger id='consumption-distribution-chart'>
            <SelectValue placeholder={t('Select default chart')} />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <SelectGroup>
              {CONSUMPTION_DISTRIBUTION_CHART_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {t(option.labelKey)}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      <div className='grid gap-1.5'>
        <Label htmlFor='model-analytics-chart'>
          {t('Default model call chart')}
        </Label>
        <Select
          items={[
            ...MODEL_ANALYTICS_CHART_OPTIONS.map((option) => ({
              value: option.value,
              label: t(option.labelKey),
            })),
          ]}
          value={draft.modelAnalyticsChart}
          onValueChange={(value) =>
            setDraft((prev) => ({
              ...prev,
              modelAnalyticsChart: value as ModelAnalyticsChartTab,
            }))
          }
        >
          <SelectTrigger id='model-analytics-chart'>
            <SelectValue placeholder={t('Select default chart')} />
          </SelectTrigger>
          <SelectContent alignItemWithTrigger={false}>
            <SelectGroup>
              {MODEL_ANALYTICS_CHART_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {t(option.labelKey)}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
    </Dialog>
  )
}
