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
import React, { useState } from 'react'

import useDialogState from '@/hooks/use-dialog'

import { type Redemption, type RedemptionsDialogType } from '../types'

type RedemptionsContextType = {
  open: RedemptionsDialogType | null
  setOpen: (str: RedemptionsDialogType | null) => void
  currentRow: Redemption | null
  setCurrentRow: React.Dispatch<React.SetStateAction<Redemption | null>>
  refreshTrigger: number
  triggerRefresh: () => void
}

const RedemptionsContext = React.createContext<RedemptionsContextType | null>(
  null
)

export function RedemptionsProvider({
  children,
}: {
  children: React.ReactNode
}) {
  const [open, setOpen] = useDialogState<RedemptionsDialogType>(null)
  const [currentRow, setCurrentRow] = useState<Redemption | null>(null)
  const [refreshTrigger, setRefreshTrigger] = useState(0)

  const triggerRefresh = () => setRefreshTrigger((prev) => prev + 1)

  return (
    <RedemptionsContext
      value={{
        open,
        setOpen,
        currentRow,
        setCurrentRow,
        refreshTrigger,
        triggerRefresh,
      }}
    >
      {children}
    </RedemptionsContext>
  )
}

// eslint-disable-next-line react-refresh/only-export-components
export const useRedemptions = () => {
  const redemptionsContext = React.useContext(RedemptionsContext)

  if (!redemptionsContext) {
    throw new Error(
      'useRedemptions has to be used within <RedemptionsProvider>'
    )
  }

  return redemptionsContext
}
