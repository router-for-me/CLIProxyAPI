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
import { createContext, useContext, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

const PageFooterContext = createContext<HTMLDivElement | null>(null)

type PageFooterProviderProps = {
  container: HTMLDivElement | null
  children: ReactNode
}

export function PageFooterProvider(props: PageFooterProviderProps) {
  return (
    <PageFooterContext.Provider value={props.container}>
      {props.children}
    </PageFooterContext.Provider>
  )
}

export function PageFooterPortal(props: { children: ReactNode }) {
  const container = useContext(PageFooterContext)
  if (!container) return null
  return createPortal(props.children, container)
}
