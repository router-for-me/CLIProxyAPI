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
import * as React from 'react'
import type { SetStateAction } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { handleServerError } from '@/lib/handle-server-error'

import { removeTrailingSlash } from './utils'
import {
  type CatalogStore,
  type PairOrphanError,
  type PairResult,
  createWaffoPancakePair,
  listWaffoPancakeCatalog,
} from './waffo-pancake-api'

export type WaffoPancakeSettingsValues = {
  WaffoPancakeMerchantID: string
  WaffoPancakePrivateKey: string
  WaffoPancakeReturnURL: string
}

export interface WaffoPancakeBinding {
  storeID: string
  productID: string
}

interface Props {
  defaultValues: WaffoPancakeSettingsValues
  values: WaffoPancakeSettingsValues
  onValueChange: <K extends keyof WaffoPancakeSettingsValues>(
    key: K,
    value: WaffoPancakeSettingsValues[K]
  ) => void
  selectedBinding: WaffoPancakeBinding
  savedBinding: WaffoPancakeBinding
  onSelectedBindingChange: (value: SetStateAction<WaffoPancakeBinding>) => void
}

const PANCAKE_DASHBOARD_URL = 'https://pancake.waffo.ai/merchant/dashboard'
const DEFAULT_NEW_STORE_NAME = 'new-api-store'
const DEFAULT_NEW_PRODUCT_NAME = 'new-api-charge-product'
const DEFAULT_NEW_PAIR_NAME = `${DEFAULT_NEW_STORE_NAME} + ${DEFAULT_NEW_PRODUCT_NAME}`

export function WaffoPancakeSettingsSection({
  defaultValues,
  values,
  onValueChange,
  selectedBinding,
  savedBinding,
  onSelectedBindingChange,
}: Props) {
  const { t } = useTranslation()

  const [phase, setPhase] = React.useState<'idle' | 'verifying'>('idle')
  const [catalog, setCatalog] = React.useState<CatalogStore[]>([])
  const [creatingPair, setCreatingPair] = React.useState(false)
  const chosenStoreID = selectedBinding.storeID
  const chosenProductID = selectedBinding.productID
  const storeID = savedBinding.storeID
  const productID = savedBinding.productID
  const returnURL = values.WaffoPancakeReturnURL

  const initialRef = React.useRef(defaultValues)
  const defaultsSignature = React.useMemo(
    () => JSON.stringify(defaultValues),
    [defaultValues]
  )

  // "merchantID|privateKey" of the last verified pair; debounced verify
  // skips when nothing changed.
  const lastVerifiedSignature = React.useRef('')
  const fetchSerialRef = React.useRef(0)

  // Mount-only — never re-sync from props after the first render. The
  // backend strips PrivateKey from GET /api/option/, so a re-sync would
  // wipe whatever the operator just typed.
  const didMountRef = React.useRef(false)
  React.useEffect(() => {
    const parsed = JSON.parse(defaultsSignature) as WaffoPancakeSettingsValues
    initialRef.current = parsed
    if (didMountRef.current) return
    didMountRef.current = true
    lastVerifiedSignature.current = `${parsed.WaffoPancakeMerchantID.trim()}|${parsed.WaffoPancakePrivateKey.trim()}`
  }, [defaultsSignature])

  const productsForChosenStore = React.useMemo(() => {
    if (!chosenStoreID) return []
    return catalog.find((s) => s.id === chosenStoreID)?.onetimeProducts ?? []
  }, [catalog, chosenStoreID])

  // Raw-ID fallback items render the trigger before the catalog loads or
  // when the saved entity has been deleted upstream.
  const storeSelectItems = React.useMemo(() => {
    const items = catalog.map((s) => ({
      value: s.id,
      label: `${s.name} (${s.id})`,
    }))
    if (chosenStoreID && !catalog.some((s) => s.id === chosenStoreID)) {
      items.push({ value: chosenStoreID, label: chosenStoreID })
    }
    return items
  }, [catalog, chosenStoreID])
  const productSelectItems = React.useMemo(() => {
    const items = productsForChosenStore.map((p) => ({
      value: p.id,
      label: `${p.name} (${p.id})`,
    }))
    if (
      chosenProductID &&
      !productsForChosenStore.some((p) => p.id === chosenProductID)
    ) {
      items.push({ value: chosenProductID, label: chosenProductID })
    }
    return items
  }, [productsForChosenStore, chosenProductID])

  // Verifies typed creds against Pancake (via /catalog) and refreshes the
  // dropdown options. `preselect` overrides the post-load anchor selection;
  // omitting it defaults to: saved binding → first store with products.
  const verifyAndFetchCatalog = React.useCallback(
    async (
      merchantID: string,
      privateKey: string,
      preselect?: { storeID?: string; productID?: string }
    ) => {
      const serial = ++fetchSerialRef.current
      let stores: CatalogStore[]
      try {
        const body = await listWaffoPancakeCatalog(merchantID, privateKey)
        if (serial !== fetchSerialRef.current) return
        if (
          body?.message === 'success' &&
          typeof body.data === 'object' &&
          body.data
        ) {
          stores = (body.data as { stores: CatalogStore[] }).stores ?? []
        } else {
          const reason = typeof body?.data === 'string' ? body.data : undefined
          handleServerError(body, undefined, {
            title: reason
              ? `${t('Credentials verification failed')}: ${reason}`
              : t(
                  'Credentials verification failed — double-check Merchant ID and API private key.'
                ),
          })
          setPhase('idle')
          return
        }
      } catch (err) {
        if (serial !== fetchSerialRef.current) return
        handleServerError(
          err,
          `${t('Credentials verification failed')}: ${
            err instanceof Error ? err.message : String(err)
          }`
        )
        setPhase('idle')
        return
      }
      if (serial !== fetchSerialRef.current) return

      setCatalog(stores)
      if (preselect) {
        onSelectedBindingChange({
          storeID: preselect.storeID ?? '',
          productID: preselect.productID ?? '',
        })
      } else {
        // Default anchor: bound product if found, else first product of
        // the first store with any — saves a click for new operators.
        const boundStore = stores.find((s) =>
          s.onetimeProducts.some((p) => p.id === productID)
        )
        if (boundStore && productID) {
          onSelectedBindingChange({
            storeID: boundStore.id,
            productID,
          })
        } else {
          const storeWithProducts = stores.find(
            (s) => s.onetimeProducts.length > 0
          )
          if (storeWithProducts) {
            onSelectedBindingChange({
              storeID: storeWithProducts.id,
              productID: storeWithProducts.onetimeProducts[0].id,
            })
          } else {
            onSelectedBindingChange({ storeID: '', productID: '' })
          }
        }
      }
      setPhase('idle')
    },
    [onSelectedBindingChange, productID, t]
  )

  const watchedMerchantID = values.WaffoPancakeMerchantID || ''
  const watchedPrivateKey = values.WaffoPancakePrivateKey || ''
  React.useEffect(() => {
    const m = watchedMerchantID.trim()
    const k = watchedPrivateKey.trim()
    if (!m || !k) return
    const signature = `${m}|${k}`
    if (signature === lastVerifiedSignature.current) return
    const timer = setTimeout(() => {
      lastVerifiedSignature.current = signature
      setPhase('verifying')
      void verifyAndFetchCatalog(m, k)
    }, 800)
    return () => clearTimeout(timer)
  }, [watchedMerchantID, watchedPrivateKey, verifyAndFetchCatalog])

  // Initial-load verify: GET /api/option/ strips PrivateKey so a returning
  // admin opens the page with empty key. Send blank creds in the body —
  // the catalog controller falls back to the persisted OptionMap creds.
  const initialLoadRef = React.useRef(false)
  React.useEffect(() => {
    if (initialLoadRef.current) return
    if (!defaultValues.WaffoPancakeMerchantID.trim()) return
    initialLoadRef.current = true
    const timer = window.setTimeout(() => {
      setPhase('verifying')
      void verifyAndFetchCatalog('', '')
    }, 0)
    return () => window.clearTimeout(timer)
  }, [defaultValues.WaffoPancakeMerchantID, verifyAndFetchCatalog])

  // Returns typed creds when the operator edited either field; otherwise
  // blanks so the backend falls back to persisted creds. Without this,
  // returning admins (saved merchant ID but empty key field) would send
  // a mixed-state body that the backend rejects.
  const readCreds = () => {
    const formMerchant = (values.WaffoPancakeMerchantID || '').trim()
    const formKey = (values.WaffoPancakePrivateKey || '').trim()
    const saved = (defaultValues.WaffoPancakeMerchantID || '').trim()
    const edited = formMerchant !== saved || formKey.length > 0
    if (!edited) return { merchantID: '', privateKey: '' }
    return { merchantID: formMerchant, privateKey: formKey }
  }

  // The minted product's SuccessURL is pinned to the current Return URL
  // field, so we prompt before creating when that field is empty.
  const handleCreatePair = async () => {
    if (!credsReady) {
      toast.error(
        t('Fill in both Merchant ID and API Private Key before creating.')
      )
      return
    }
    const { merchantID, privateKey } = readCreds()
    const trimmedReturn = removeTrailingSlash(returnURL.trim())
    if (!trimmedReturn) {
      if (
        !window.confirm(
          t(
            'Payment return URL is empty. Create the product without a SuccessURL redirect?'
          )
        )
      ) {
        return
      }
    }
    setCreatingPair(true)
    try {
      const body = await createWaffoPancakePair({
        merchantID,
        privateKey,
        returnURL: trimmedReturn,
      })
      if (
        body?.message === 'success' &&
        typeof body.data === 'object' &&
        body.data
      ) {
        const created = body.data as PairResult
        // Refetch from GraphQL rather than trusting the response body so the
        // dropdowns reflect authoritative state, then anchor on minted IDs.
        setPhase('verifying')
        await verifyAndFetchCatalog(merchantID, privateKey, {
          storeID: created.store_id,
          productID: created.product_id,
        })
        toast.success(
          `${t('Store + product created')}: ${created.store_id} / ${created.product_id}`
        )
        return
      }
      const errData =
        body && typeof body.data === 'object' && body.data !== null
          ? (body.data as PairOrphanError)
          : null
      if (errData?.orphan_store && errData.store_id) {
        setPhase('verifying')
        await verifyAndFetchCatalog(merchantID, privateKey, {
          storeID: errData.store_id,
          productID: '',
        })
      }
      const reason =
        errData?.error ??
        (typeof body?.data === 'string' ? body.data : undefined)
      handleServerError(body, undefined, {
        title: reason
          ? `${t('Creation failed')}: ${reason}`
          : t('Creation failed'),
      })
    } catch (err) {
      handleServerError(
        err,
        `${t('Creation failed')}: ${err instanceof Error ? err.message : String(err)}`
      )
    } finally {
      setCreatingPair(false)
    }
  }

  const verifying = phase === 'verifying'

  // "Not edited" = MerchantID unchanged AND PrivateKey field blank, in
  // which case the backend falls back to persisted creds. Otherwise we
  // require both fields filled (mixed states would fail signature check).
  const savedMerchantID = (defaultValues.WaffoPancakeMerchantID || '').trim()
  const formMerchantID = watchedMerchantID.trim()
  const formPrivateKey = watchedPrivateKey.trim()
  const credsEdited =
    formMerchantID !== savedMerchantID || formPrivateKey.length > 0
  const hasSavedCreds = savedMerchantID.length > 0
  const credsReady = credsEdited
    ? formMerchantID.length > 0 && formPrivateKey.length > 0
    : hasSavedCreds
  const hasCatalog = catalog.length > 0

  let bindStatusMessage: string
  if (!credsReady) {
    bindStatusMessage = t('Fill in the credentials above to begin.')
  } else if (verifying) {
    bindStatusMessage = t(
      'Verifying credentials and pulling stores from your Pancake account...'
    )
  } else if (hasCatalog) {
    bindStatusMessage = t(
      'Mint a fresh pair below — or pick an existing one further down. Click Save when ready.'
    )
  } else {
    bindStatusMessage = t(
      'No stores on this merchant yet. Set a return URL and click Create to mint your first pair.'
    )
  }

  return (
    <div className='space-y-4 pt-4'>
      <div>
        <h3 className='text-lg font-medium'>{t('Waffo Pancake MoR')}</h3>
        <p className='text-muted-foreground text-sm'>
          {t(
            'Start collecting payments globally without registering a company. Built for indie developers, OPC sole proprietorships, and startups. Waffo Pancake acts as your Merchant of Record, taking on the compliance burden of global payment collection — consumption tax, invoicing, subscription management, refunds, and chargebacks. Solo developers can launch fast and stay focused on product instead of compliance. Onboard in minutes — one prompt to a full integration.'
          )}
        </p>
      </div>
      <div className='grid min-w-0 gap-x-5 gap-y-4 lg:grid-cols-2'>
        {/* Blue box — webhook configuration only. */}
        <div className='rounded-md bg-blue-50 p-4 text-sm text-blue-900 lg:col-span-2 dark:bg-blue-950 dark:text-blue-100'>
          <p className='mb-2 font-medium'>{t('Webhook Configuration:')}</p>
          <ul className='list-inside list-disc space-y-1'>
            <li>
              {t('Webhook URL (Test):')}{' '}
              <code className='rounded bg-blue-100 px-1 py-0.5 text-xs dark:bg-blue-900'>
                {'<ServerAddress>/api/waffo-pancake/webhook/test'}
              </code>
            </li>
            <li>
              {t('Webhook URL (Production):')}{' '}
              <code className='rounded bg-blue-100 px-1 py-0.5 text-xs dark:bg-blue-900'>
                {'<ServerAddress>/api/waffo-pancake/webhook/prod'}
              </code>
            </li>
            <li>
              {t(
                'Register each URL into the matching Test Mode / Production Mode webhook slot in the Pancake dashboard. Separate endpoints prevent test traffic from accidentally crediting production accounts.'
              )}
            </li>
            <li>
              {t('Configure at:')}{' '}
              <a
                href={PANCAKE_DASHBOARD_URL}
                target='_blank'
                rel='noreferrer'
                className='underline hover:no-underline'
              >
                {t('Waffo Pancake Dashboard')}
              </a>
            </li>
          </ul>
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('Merchant ID')}</Label>
          <Input
            placeholder='MER_xxx'
            autoComplete='off'
            value={values.WaffoPancakeMerchantID}
            onChange={(event) =>
              onValueChange('WaffoPancakeMerchantID', event.target.value)
            }
          />
        </div>

        <div className='grid gap-1.5'>
          <Label>{t('API Private Key')}</Label>
          <Textarea
            rows={4}
            placeholder={t('Leave blank to keep the existing key')}
            autoComplete='new-password'
            value={values.WaffoPancakePrivateKey}
            onChange={(event) =>
              onValueChange('WaffoPancakePrivateKey', event.target.value)
            }
            className='font-mono text-xs'
          />
          <p className='text-muted-foreground text-xs'>
            {t(
              'The environment (test vs production) is decided by the key you paste here — use the Test key while integrating, then swap to the Production key when going live.'
            )}
          </p>
        </div>

        {/*
          Binding section — split into two visually distinct paths:
          (A) "Use existing" pair from the loaded catalog — only rendered when
              the merchant actually has stores, so first-time setup isn't
              cluttered by dead dropdowns.
          (B) "Create a fresh pair" — always available, paired with the
              return URL field that's only meaningful here.
          The two paths are split by an "or" divider so the operator never has
          to wonder which field belongs to which intent.
        */}
        <div className='space-y-4 pt-2 lg:col-span-2'>
          <div>
            <h4 className='font-medium'>
              {t('Bind a Pancake store + product')}
            </h4>
            <p className='text-muted-foreground text-xs'>{bindStatusMessage}</p>
          </div>

          {/*
              Operator-facing explainer: why only ONE store + product needs
              to be bound at the gateway level, and what each piece is used
              for. Subscriptions reuse the same Store but get their own
              per-plan product, configured in the Subscriptions admin.
            */}
          <div className='rounded-md border border-blue-200 bg-blue-50 p-3 text-xs text-blue-900 dark:border-blue-900/60 dark:bg-blue-950/40 dark:text-blue-100'>
            <p className='mb-1 font-medium'>
              {t('Why only one store + product?')}
            </p>
            <ul className='list-inside list-disc space-y-1'>
              <li>
                {t(
                  'The bound Store is the parent container for every Pancake product new-api creates from this admin — both the wallet top-up product and any subscription-plan products. One store is enough; pin a different one only if you genuinely run separate Pancake catalogs.'
                )}
              </li>
              <li>
                {t(
                  'The bound Product powers wallet top-ups: when a user enters any amount, new-api runs the checkout against this single Pancake product and overrides the price per session — no need to pre-create $1 / $5 / $10 SKUs.'
                )}
              </li>
              <li>
                {t(
                  'Subscription plans do NOT use the bound Product — each plan has its own dedicated Pancake product, set in the Subscriptions admin (or auto-minted via the "+ Create" button there).'
                )}
              </li>
            </ul>
          </div>

          {/* Create section — first, since creating auto-fills the pick-existing dropdowns below. */}
          <div className='space-y-1.5'>
            <Label>{t('Payment return URL')}</Label>
            <div className='flex gap-2'>
              <Input
                placeholder='https://example.com/wallet'
                value={returnURL}
                onChange={(event) =>
                  onValueChange('WaffoPancakeReturnURL', event.target.value)
                }
                className='flex-1'
              />
              <Button
                type='button'
                variant='outline'
                onClick={handleCreatePair}
                disabled={creatingPair || verifying || !credsReady}
                className='shrink-0'
              >
                {creatingPair
                  ? t('Creating...')
                  : `+ ${t('Create')} ${DEFAULT_NEW_PAIR_NAME}`}
              </Button>
            </div>
            <p className='text-muted-foreground text-xs'>
              {t(
                "Used as SuccessURL on the new product. You'll be prompted to confirm if left blank."
              )}
            </p>
          </div>

          {hasCatalog ? (
            <>
              <div className='relative flex items-center py-1'>
                <div className='flex-1 border-t' />
                <span className='text-muted-foreground px-3 text-[10px] font-medium tracking-[0.2em] uppercase'>
                  {t('or pick existing')}
                </span>
                <div className='flex-1 border-t' />
              </div>

              <div className='grid grid-cols-2 gap-3'>
                <div className='grid gap-1.5'>
                  <Label>{t('Store')}</Label>
                  <Combobox
                    options={storeSelectItems}
                    value={chosenStoreID}
                    onValueChange={(value) => {
                      // Base UI Select can deliver null on deselect.
                      onSelectedBindingChange({
                        storeID: value ?? '',
                        productID: '',
                      })
                    }}
                    className='w-full'
                    placeholder={t('Select a store')}
                  />
                </div>

                <div className='grid gap-1.5'>
                  <Label>{t('Product')}</Label>
                  <Combobox
                    options={productSelectItems}
                    value={chosenProductID}
                    onValueChange={(value) =>
                      onSelectedBindingChange((previous) => ({
                        ...previous,
                        productID: value ?? '',
                      }))
                    }
                    disabled={!chosenStoreID || productSelectItems.length === 0}
                    className='w-full'
                    placeholder={t('Select a product')}
                  />
                </div>
              </div>
            </>
          ) : null}

          <div className='flex items-center gap-3'>
            {storeID || productID ? (
              <div className='text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs'>
                {storeID ? (
                  <span>
                    {t('Bound store:')}{' '}
                    <code className='bg-muted rounded px-1 py-0.5'>
                      {storeID}
                    </code>
                  </span>
                ) : null}
                {productID ? (
                  <span>
                    {t('Bound product:')}{' '}
                    <code className='bg-muted rounded px-1 py-0.5'>
                      {productID}
                    </code>
                  </span>
                ) : null}
              </div>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  )
}
