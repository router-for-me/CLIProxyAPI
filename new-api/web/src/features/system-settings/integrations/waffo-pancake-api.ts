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

// Catalog / pair / save admin endpoints. Match
// controller/topup_waffo_pancake.go: empty body creds make the backend
// fall back to persisted OptionMap values, so returning admins don't
// have to re-paste the private key (stripped from GET /api/option/).

export interface CatalogProduct {
  id: string
  name: string
  status: string
}

export interface CatalogStore {
  id: string
  name: string
  status: string
  prodEnabled: boolean
  onetimeProducts: CatalogProduct[]
}

export interface PairResult {
  store_id: string
  store_name: string
  product_id: string
  product_name: string
}

export interface PairOrphanError {
  error?: string
  orphan_store?: boolean
  store_id?: string
  store_name?: string
}

interface BackendBody<T> {
  message?: string
  data?: T | string
}

export type CatalogResponse = BackendBody<{ stores: CatalogStore[] }>
export type PairResponse = BackendBody<PairResult>
export type SaveResponse = BackendBody<{ product_id: string; store_id: string }>

export async function listWaffoPancakeCatalog(
  merchantID: string,
  privateKey: string
): Promise<CatalogResponse> {
  const res = await api.get<CatalogResponse>(
    '/api/option/waffo-pancake/catalog',
    { params: { merchant_id: merchantID, private_key: privateKey } }
  )
  return res.data
}

export async function createWaffoPancakePair(params: {
  merchantID: string
  privateKey: string
  returnURL: string
}): Promise<PairResponse> {
  const res = await api.post<PairResponse>('/api/option/waffo-pancake/pair', {
    merchant_id: params.merchantID,
    private_key: params.privateKey,
    return_url: params.returnURL,
  })
  return res.data
}

export async function saveWaffoPancakeConfig(params: {
  merchantID: string
  privateKey: string
  returnURL: string
  storeID: string
  productID: string
}): Promise<SaveResponse> {
  const res = await api.post<SaveResponse>('/api/option/waffo-pancake/save', {
    merchant_id: params.merchantID,
    private_key: params.privateKey,
    return_url: params.returnURL,
    store_id: params.storeID,
    product_id: params.productID,
  })
  return res.data
}
