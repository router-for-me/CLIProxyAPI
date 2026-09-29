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
import { LinkSquare01Icon } from '@hugeicons/core-free-icons'
import { HugeiconsIcon } from '@hugeicons/react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

import { getPluginWebsite } from '../lib/plugin-website'

export function PluginWebsiteLink(props: { website?: string }) {
  const { t } = useTranslation()
  const website = getPluginWebsite(props.website)
  if (!website) return null

  return (
    <Button
      role='link'
      variant='link'
      size='xs'
      className='h-auto max-w-full justify-start px-0 text-xs whitespace-normal'
      render={<a href={website} target='_blank' rel='noopener noreferrer' />}
    >
      <HugeiconsIcon icon={LinkSquare01Icon} aria-hidden='true' />
      {t('Plugin website')}
    </Button>
  )
}
