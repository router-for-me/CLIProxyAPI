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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useMemo, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ErrorState } from '@/components/error-state'
import { LanguageSwitcher } from '@/components/language-switcher'
import { LoadingState } from '@/components/loading-state'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Form } from '@/components/ui/form'
import { Skeleton } from '@/components/ui/skeleton'
import { useSystemConfig } from '@/hooks/use-system-config'
import { handleServerError } from '@/lib/handle-server-error'
import { accountPasswordSchema } from '@/lib/password-policy'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { buildSetupPayload, getSetupStatus, submitSetup } from './api'
import { AdminStep } from './components/admin-step'
import { CompleteStep } from './components/complete-step'
import { DatabaseStep } from './components/database-step'
import { StepNavigation } from './components/step-navigation'
import { UsageModeStep } from './components/usage-mode-step'
import type { SetupFormValues, SetupStatus } from './types'

const STEPS = [
  {
    titleKey: 'Database check',
    descriptionKey: 'Verify your database connection',
  },
  {
    titleKey: 'Administrator account',
    descriptionKey: 'Create credentials for the root user',
  },
  {
    titleKey: 'Usage mode',
    descriptionKey: 'Choose how the platform will operate',
  },
  {
    titleKey: 'Review & initialize',
    descriptionKey: 'Confirm settings and finish setup',
  },
]

const DEFAULT_FORM_VALUES: SetupFormValues = {
  username: '',
  password: '',
  confirmPassword: '',
  usageMode: 'external',
}

export function SetupWizard() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const { systemName, logo, loading: systemConfigLoading } = useSystemConfig()

  const [currentStep, setCurrentStep] = useState(0)
  const [setupStatus, setSetupStatus] = useState<SetupStatus | undefined>()

  const form = useForm<SetupFormValues>({
    defaultValues: DEFAULT_FORM_VALUES,
    mode: 'onBlur',
  })

  const watchedValues = form.watch()

  const {
    data: statusResponse,
    isLoading,
    isError,
    refetch,
  } = useQuery({
    queryKey: ['setup-status'],
    queryFn: async () => requireServerSuccess(await getSetupStatus()),
    retry: false,
  })

  const mutation = useMutation({
    mutationKey: ['setup-submit'],
    mutationFn: submitSetup,
    onSuccess: async (response) => {
      if (response.success) {
        toast.success(t('System initialized successfully! Redirecting…'))
        await queryClient.invalidateQueries({ queryKey: ['setup-status'] })
        setTimeout(() => {
          navigate({ to: '/' })
        }, 1200)
      } else {
        handleServerError(
          response,
          t('Initialization failed, please try again.')
        )
      }
    },
    onError: (error) => {
      handleServerError(error, t('Failed to initialize system'))
    },
  })

  useEffect(() => {
    if (!statusResponse) return

    if (!statusResponse.success) {
      handleServerError(statusResponse, t('Failed to load setup status'))
      return
    }

    const status = statusResponse.data
    if (!status) return

    if (status.status) {
      navigate({ to: '/' })
      return
    }

    setSetupStatus(status)
    setCurrentStep(0)

    // Pre-fill usage mode if backend echoes it
    if (status.SelfUseModeEnabled) {
      form.setValue('usageMode', 'self', {
        shouldDirty: false,
        shouldTouch: false,
        shouldValidate: false,
      })
    } else if (status.DemoSiteEnabled) {
      form.setValue('usageMode', 'demo', {
        shouldDirty: false,
        shouldTouch: false,
        shouldValidate: false,
      })
    } else {
      form.setValue('usageMode', 'external', {
        shouldDirty: false,
        shouldTouch: false,
        shouldValidate: false,
      })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [statusResponse, navigate, form])

  useEffect(() => {
    if (!setupStatus) return

    // Reset admin fields when backend reports they are already initialized
    if (setupStatus.root_init) {
      form.setValue('username', '', {
        shouldDirty: false,
        shouldTouch: false,
        shouldValidate: false,
      })
      form.setValue('password', '', {
        shouldDirty: false,
        shouldTouch: false,
        shouldValidate: false,
      })
      form.setValue('confirmPassword', '', {
        shouldDirty: false,
        shouldTouch: false,
        shouldValidate: false,
      })
    }
  }, [setupStatus, form])

  const currentStepComponent = useMemo(() => {
    if (currentStep === 0) {
      return <DatabaseStep status={setupStatus} />
    }
    if (currentStep === 1) {
      return (
        <AdminStep
          form={form}
          rootInitialized={Boolean(setupStatus?.root_init)}
        />
      )
    }
    if (currentStep === 2) {
      return <UsageModeStep form={form} />
    }
    return <CompleteStep status={setupStatus} values={watchedValues} />
  }, [currentStep, setupStatus, form, watchedValues])

  const validateAdminStep = () => {
    if (setupStatus?.root_init) return true

    const username = form.getValues('username')?.trim()
    const password = form.getValues('password')
    const confirmPassword = form.getValues('confirmPassword')

    if (!username) {
      form.setError('username', {
        type: 'manual',
        message: t('Please enter an administrator username'),
      })
      toast.error(t('Please enter an administrator username'))
      return false
    }

    if (!accountPasswordSchema.safeParse(password).success) {
      form.setError('password', {
        type: 'manual',
        message: t('Password must contain between 8 and 128 characters.'),
      })
      toast.error(t('Password must contain between 8 and 128 characters.'))
      return false
    }

    if (password !== confirmPassword) {
      form.setError('confirmPassword', {
        type: 'manual',
        message: t('Passwords do not match'),
      })
      toast.error(t('Passwords do not match'))
      return false
    }

    return true
  }

  const validateUsageModeStep = () => {
    const usageMode = form.getValues('usageMode')
    if (!usageMode) {
      form.setError('usageMode', {
        type: 'manual',
        message: t('Select a usage mode to continue'),
      })
      toast.error(t('Select a usage mode to continue'))
      return false
    }
    return true
  }

  const handleNextStep = () => {
    if (currentStep === 1 && !validateAdminStep()) return
    if (currentStep === 2 && !validateUsageModeStep()) return

    setCurrentStep((step) => Math.min(step + 1, STEPS.length - 1))
  }

  const handlePreviousStep = () => {
    setCurrentStep((step) => Math.max(step - 1, 0))
  }

  const handleSubmit = async () => {
    const adminValid = validateAdminStep()
    const usageValid = validateUsageModeStep()
    if (!adminValid || !usageValid) return

    const payload = buildSetupPayload(
      form.getValues(),
      Boolean(setupStatus?.root_init)
    )

    mutation.mutate(payload)
  }

  return (
    <div className='bg-muted/40 relative min-h-svh py-10'>
      <div className='absolute top-4 right-4 sm:top-6 sm:right-6'>
        <LanguageSwitcher />
      </div>
      <div className='container mx-auto flex max-w-5xl flex-col gap-8 px-4 sm:px-6'>
        <div className='flex flex-col items-center gap-3'>
          <div className='relative h-12 w-12'>
            {systemConfigLoading ? (
              <Skeleton className='absolute inset-0 rounded-full' />
            ) : (
              <img
                src={logo}
                alt={t('System logo')}
                className='h-12 w-12 rounded-full object-cover shadow-sm'
              />
            )}
          </div>
          {systemConfigLoading ? (
            <Skeleton className='h-7 w-40' />
          ) : (
            <h1 className='text-2xl font-semibold tracking-tight'>
              {t('Initialize')} {systemName}
            </h1>
          )}
          <p className='text-muted-foreground text-center text-sm sm:text-base'>
            {t(
              'Follow the guided steps to prepare your workspace before the first login.'
            )}
          </p>
        </div>

        <Card className='shadow-lg'>
          <CardHeader className='space-y-2'>
            <CardTitle className='text-xl font-semibold'>
              {t('System setup wizard')}
            </CardTitle>
            <CardDescription>
              {t('Complete these steps to finish the initial installation.')}
            </CardDescription>
          </CardHeader>

          <CardContent className='space-y-6'>
            <ol className='grid gap-3 sm:grid-cols-4'>
              {STEPS.map((step, index) => {
                const isActive = currentStep === index
                const isCompleted = currentStep > index
                return (
                  <li
                    key={step.titleKey}
                    className={cn('rounded-xl border p-3', {
                      'border-primary ring-primary/20 ring-2': isActive,
                      'border-primary/40 bg-primary/5':
                        !isActive && isCompleted,
                      'border-muted bg-card': !isActive && !isCompleted,
                    })}
                  >
                    <div className='flex items-start gap-3'>
                      <span
                        className={cn(
                          'flex size-6 items-center justify-center rounded-md border text-xs font-semibold',
                          isActive || isCompleted
                            ? 'border-primary bg-primary text-primary-foreground'
                            : 'border-muted-foreground/40 text-muted-foreground'
                        )}
                      >
                        {index + 1}
                      </span>
                      <div className='space-y-1'>
                        <p className='text-sm font-semibold'>
                          {t(step.titleKey)}
                        </p>
                        <p className='text-muted-foreground text-xs'>
                          {t(step.descriptionKey)}
                        </p>
                      </div>
                    </div>
                  </li>
                )
              })}
            </ol>

            {isLoading && <LoadingState message={t('Loading setup status…')} />}
            {!isLoading && isError && (
              <ErrorState
                title={t('We could not load the setup status.')}
                onRetry={() => refetch()}
              />
            )}
            {!isLoading && !isError && (
              <Form {...form}>
                <form
                  className='space-y-6'
                  onSubmit={(event) => event.preventDefault()}
                >
                  {currentStepComponent}
                </form>
              </Form>
            )}
          </CardContent>

          {!isLoading && !isError && (
            <CardFooter className='w-full justify-end border-t'>
              <StepNavigation
                currentStep={currentStep}
                totalSteps={STEPS.length}
                onBack={handlePreviousStep}
                onNext={handleNextStep}
                onSubmit={handleSubmit}
                isSubmitting={mutation.isPending}
              />
            </CardFooter>
          )}
        </Card>
      </div>
    </div>
  )
}
