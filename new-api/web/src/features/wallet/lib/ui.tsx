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
import i18next from 'i18next'
import { CreditCard, Landmark } from 'lucide-react'
import { type ReactNode } from 'react'
import { SiAlipay, SiWechat, SiStripe } from 'react-icons/si'

import { ReactIconByName } from '@/components/react-icon-by-name'

import { PAYMENT_TYPES, PAYMENT_ICON_COLORS } from '../constants'

// ============================================================================
// UI Helper Functions
// ============================================================================

/**
 * Resolves a backend-provided image URL to https only. Rejects http:,
 * data:, blob:, file:, relative paths, and URLs with userinfo, which are unsafe
 * or ambiguous in <img src/>.
 */
function normalizeHttpIconUrl(raw: string | undefined | null): string | null {
  if (!raw) return null
  const s = raw.trim()
  if (!s) return null
  if (!/^https:\/\//i.test(s)) return null

  let url: URL
  try {
    url = new URL(s)
  } catch {
    return null
  }
  if (url.protocol !== 'https:') {
    return null
  }
  if (url.username || url.password) {
    return null
  }
  return url.toString()
}

/**
 * Get payment method icon component
 *
 * When icon is provided, render a safe http(s) image URL or resolve it as a
 * react-icons component name. Invalid configured icons intentionally render
 * nothing instead of falling back to the payment type.
 */
export function getPaymentIcon(
  paymentType: string | undefined,
  className: string = 'h-4 w-4',
  icon?: string,
  altName?: string
): ReactNode {
  const iconValue = icon?.trim()
  const safeIconUrl = normalizeHttpIconUrl(iconValue)
  if (safeIconUrl) {
    return (
      <img
        src={safeIconUrl}
        alt={altName || paymentType || 'payment'}
        className={className}
        style={{ objectFit: 'contain' }}
        loading='lazy'
        decoding='async'
        referrerPolicy='no-referrer'
      />
    )
  }
  if (iconValue) {
    return (
      <ReactIconByName
        name={iconValue}
        className={className}
        title={altName || paymentType || iconValue}
      />
    )
  }

  if (!paymentType) {
    return <CreditCard className={className} />
  }

  switch (paymentType) {
    case PAYMENT_TYPES.ALIPAY:
      return (
        <SiAlipay
          className={className}
          style={{ color: PAYMENT_ICON_COLORS[PAYMENT_TYPES.ALIPAY] }}
        />
      )
    case PAYMENT_TYPES.WECHAT:
      return (
        <SiWechat
          className={className}
          style={{ color: PAYMENT_ICON_COLORS[PAYMENT_TYPES.WECHAT] }}
        />
      )
    case PAYMENT_TYPES.STRIPE:
      return (
        <SiStripe
          className={className}
          style={{ color: PAYMENT_ICON_COLORS[PAYMENT_TYPES.STRIPE] }}
        />
      )
    case PAYMENT_TYPES.CREEM:
      return (
        <Landmark
          className={className}
          style={{ color: PAYMENT_ICON_COLORS[PAYMENT_TYPES.CREEM] }}
        />
      )
    case PAYMENT_TYPES.WAFFO:
      return (
        <CreditCard
          className={className}
          style={{ color: PAYMENT_ICON_COLORS[PAYMENT_TYPES.WAFFO] }}
        />
      )
    case PAYMENT_TYPES.WAFFO_PANCAKE:
      // The W glyph fills only ~40% of its viewBox vertically (wide and
      // short letterform); scale(2) brings its rendered height in line
      // with Stripe's S and Creem's Landmark.
      return (
        <span
          className={`inline-flex items-center justify-center leading-none ${className}`}
          style={{ transform: 'scale(2)' }}
        >
          <img
            src='/waffo-logo-light.svg'
            alt={i18next.t('Waffo')}
            className='block h-full w-full object-contain dark:hidden'
          />
          <img
            src='/waffo-logo-dark.svg'
            alt={i18next.t('Waffo')}
            className='hidden h-full w-full object-contain dark:block'
          />
        </span>
      )
    default:
      return <CreditCard className={className} />
  }
}
