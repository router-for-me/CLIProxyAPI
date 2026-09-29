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
import { Combobox } from '@/components/ui/combobox'
import { useState } from 'react'
import type { UseFormReturn } from 'react-hook-form'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'


import { SettingsControlGroup } from '../../../components/settings-form-layout'
import { OAUTH_PRESETS, type CustomOAuthFormValues } from '../types'

type PresetSelectorProps = {
  form: UseFormReturn<CustomOAuthFormValues>
}

export function PresetSelector(props: PresetSelectorProps) {
  const { t } = useTranslation()
  const [selectedPreset, setSelectedPreset] = useState<string>('')
  const [baseUrl, setBaseUrl] = useState<string>('')

  const handlePresetChange = (presetKey: string) => {
    setSelectedPreset(presetKey)
    const preset = OAUTH_PRESETS.find((p) => p.key === presetKey)
    if (!preset) return

    // Auto-fill name, slug, icon, and field mappings immediately
    props.form.setValue('name', preset.name, { shouldDirty: true })
    props.form.setValue(
      'slug',
      presetKey.toLowerCase().replaceAll(/\s+/g, '-'),
      {
        shouldDirty: true,
      }
    )
    props.form.setValue('icon', preset.icon, { shouldDirty: true })
    props.form.setValue('scopes', preset.scopes, { shouldDirty: true })
    props.form.setValue('user_id_field', preset.user_id_field, {
      shouldDirty: true,
    })
    props.form.setValue('username_field', preset.username_field, {
      shouldDirty: true,
    })
    props.form.setValue('display_name_field', preset.display_name_field, {
      shouldDirty: true,
    })
    props.form.setValue('email_field', preset.email_field, {
      shouldDirty: true,
    })

    // Apply base URL if already entered
    if (baseUrl) {
      applyEndpoints(preset, baseUrl)
    }
  }

  const handleBaseUrlChange = (url: string) => {
    setBaseUrl(url)
    if (!selectedPreset) return

    const preset = OAUTH_PRESETS.find((p) => p.key === selectedPreset)
    if (!preset) return

    applyEndpoints(preset, url)
  }

  const applyEndpoints = (
    preset: (typeof OAUTH_PRESETS)[number],
    url: string
  ) => {
    const cleanUrl = url.replace(/\/+$/, '')
    props.form.setValue(
      'authorization_endpoint',
      cleanUrl + preset.authorization_endpoint,
      { shouldDirty: true }
    )
    props.form.setValue('token_endpoint', cleanUrl + preset.token_endpoint, {
      shouldDirty: true,
    })
    props.form.setValue(
      'user_info_endpoint',
      cleanUrl + preset.user_info_endpoint,
      { shouldDirty: true }
    )
  }

  return (
    <SettingsControlGroup className='space-y-3 border-dashed'>
      <p className='text-sm font-medium'>{t('Quick Setup from Preset')}</p>
      <div className='grid grid-cols-1 gap-3 sm:grid-cols-2'>
        <div className='space-y-1.5'>
          <Label>{t('Preset Template')}</Label>
          <Combobox
 options={OAUTH_PRESETS.map((preset) => ({ value: preset.key, label: preset.name }))}
 value={selectedPreset}
 onValueChange={(value) => { if (value !== null) handlePresetChange(value) }}
 aria-label={t('Select preset')}
 placeholder={t('Select preset')}
 className='w-full'
/>
        </div>
        <div className='space-y-1.5'>
          <Label>{t('Base URL')}</Label>
          <Input
            placeholder={t('https://your-server.example.com')}
            value={baseUrl}
            onChange={(e) => handleBaseUrlChange(e.target.value)}
          />
        </div>
      </div>
    </SettingsControlGroup>
  )
}
