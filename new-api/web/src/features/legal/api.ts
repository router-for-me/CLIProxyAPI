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
import { api } from '@/lib/api'

import type { LegalDocumentResponse } from './types'

// Both documents drop the client's global `Cache-Control: no-store` for the
// same reason as getNotice in @/lib/api: `no-store` stops the browser from
// keeping a copy, so it would never hold an ETag to revalidate with and the
// server could never answer 304. These are the largest payloads in this family
// and are re-fetched on every sign-up, so the saving is the most visible here.
export async function getUserAgreement(): Promise<LegalDocumentResponse> {
  const res = await api.get<LegalDocumentResponse>('/api/user-agreement', {
    headers: { 'Cache-Control': null },
  })
  return res.data
}

export async function getPrivacyPolicy(): Promise<LegalDocumentResponse> {
  const res = await api.get<LegalDocumentResponse>('/api/privacy-policy', {
    headers: { 'Cache-Control': null },
  })
  return res.data
}
