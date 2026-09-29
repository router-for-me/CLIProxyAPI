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
import { useQueryClient } from '@tanstack/react-query'
import { getRouteApi, useNavigate } from '@tanstack/react-router'
import { Plus } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionPageLayout } from '@/components/layout'
import { Button } from '@/components/ui/button'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { requireServerSuccess } from '@/lib/server-error-message'

import { listDeployments } from './api'
import { DeploymentAccessGuard } from './components/deployment-access-guard'
import { DeploymentsTable } from './components/deployments-table'
import { CreateDeploymentDrawer } from './components/dialogs/create-deployment-drawer'
import { ModelsDialogs } from './components/models-dialogs'
import { ModelsPrimaryButtons } from './components/models-primary-buttons'
import { ModelsProvider, useModels } from './components/models-provider'
import { ModelsTable } from './components/models-table'
import { VendorsTable } from './components/vendors-table'
import { useModelDeploymentSettings } from './hooks/use-model-deployment-settings'
import { deploymentsQueryKeys } from './lib'
import {
  type ModelsSectionId,
  MODELS_DEFAULT_SECTION,
  MODELS_SECTION_IDS,
} from './section-registry'

const route = getRouteApi('/_authenticated/models/$section')

const SECTION_META: Record<
  ModelsSectionId,
  { titleKey: string; tabKey: string }
> = {
  metadata: {
    titleKey: 'Model management',
    tabKey: 'Models',
  },
  vendors: { titleKey: 'Vendor management', tabKey: 'Vendors' },
  deployments: {
    titleKey: 'Deployments',
    tabKey: 'Deployments',
  },
}

function ModelsContent() {
  const { t } = useTranslation()
  const navigate = useNavigate({ from: '/models/$section' })
  const { tabCategory, setTabCategory, setOpen, setCurrentVendor } = useModels()
  const params = route.useParams()
  const activeSection = (params.section ??
    MODELS_DEFAULT_SECTION) as ModelsSectionId

  // Deployment create dialog state
  const [createDeploymentOpen, setCreateDeploymentOpen] = useState(false)

  // keep context state in sync (for components that rely on it)
  useEffect(() => {
    if (tabCategory !== activeSection) {
      setTabCategory(activeSection)
    }
  }, [activeSection, setTabCategory, tabCategory])

  const handleSectionChange = useCallback(
    (section: string) => {
      void navigate({
        to: '/models/$section',
        params: { section: section as ModelsSectionId },
        search: (previous) => previous,
      })
    },
    [navigate]
  )

  const meta = SECTION_META[activeSection] ?? SECTION_META.metadata

  let actions = <ModelsPrimaryButtons />
  let content = <ModelsTable />
  if (activeSection === 'vendors') {
    actions = (
      <Button
        size='sm'
        onClick={() => {
          setCurrentVendor(null)
          setOpen('create-vendor')
        }}
      >
        <Plus className='size-4' />
        {t('Add Vendor')}
      </Button>
    )
    content = <VendorsTable />
  } else if (activeSection === 'deployments') {
    actions = (
      <Button onClick={() => setCreateDeploymentOpen(true)} size='sm'>
        <Plus className='size-4' />
        {t('Create deployment')}
      </Button>
    )
    content = <DeploymentsSection />
  }

  return (
    <>
      <SectionPageLayout
        fixedContent
        stackActionsOnMobile={activeSection === 'metadata'}
      >
        <SectionPageLayout.Title>{t(meta.titleKey)}</SectionPageLayout.Title>
        <SectionPageLayout.Actions>{actions}</SectionPageLayout.Actions>
        <SectionPageLayout.Content>
          <div className='flex h-full min-h-0 flex-col gap-4'>
            <Tabs value={activeSection} onValueChange={handleSectionChange}>
              <TabsList className='max-w-full flex-wrap justify-start group-data-horizontal/tabs:h-auto'>
                {MODELS_SECTION_IDS.map((section) => (
                  <TabsTrigger key={section} value={section}>
                    {t(SECTION_META[section].tabKey)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
            <div className='min-h-0 flex-1'>{content}</div>
          </div>
        </SectionPageLayout.Content>
      </SectionPageLayout>

      <ModelsDialogs />
      <CreateDeploymentDrawer
        open={createDeploymentOpen}
        onOpenChange={setCreateDeploymentOpen}
      />
    </>
  )
}

function DeploymentsSection() {
  const queryClient = useQueryClient()
  const {
    loading: deploymentLoading,
    loadingPhase,
    isIoNetEnabled,
    connectionLoading,
    connectionOk,
    connectionError,
    testConnection,
  } = useModelDeploymentSettings()

  // Prefetch deployments list while connection check is in progress.
  useEffect(() => {
    if (isIoNetEnabled && loadingPhase === 'connection') {
      const defaultParams = { p: 1, page_size: 10 }
      queryClient.prefetchQuery({
        queryKey: deploymentsQueryKeys.list(defaultParams),
        queryFn: async () =>
          requireServerSuccess(await listDeployments(defaultParams)),
        staleTime: 30 * 1000,
      })
    }
  }, [isIoNetEnabled, loadingPhase, queryClient])

  return (
    <DeploymentAccessGuard
      loading={deploymentLoading}
      loadingPhase={loadingPhase}
      isEnabled={isIoNetEnabled}
      connectionLoading={connectionLoading}
      connectionOk={connectionOk}
      connectionError={connectionError}
      onRetry={testConnection}
    >
      <DeploymentsTable />
    </DeploymentAccessGuard>
  )
}

export function Models() {
  return (
    <ModelsProvider>
      <ModelsContent />
    </ModelsProvider>
  )
}
