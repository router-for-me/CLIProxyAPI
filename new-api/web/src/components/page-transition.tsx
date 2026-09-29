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
import { Outlet, useRouterState } from '@tanstack/react-router'
import { motion, useReducedMotion, type Variants } from 'motion/react'
import type { ReactNode } from 'react'

import {
  CARD_ITEM_VARIANTS,
  CARD_STAGGER_VARIANTS,
  MOTION_TRANSITION,
  MOTION_VARIANTS,
  STAGGER_ITEM_VARIANTS,
  STAGGER_VARIANTS,
  TABLE_ROW_VARIANTS,
  TABLE_STAGGER_VARIANTS,
} from '@/lib/motion'

interface PageTransitionProps {
  children: ReactNode
  className?: string
}

export function PageTransition(props: PageTransitionProps) {
  const shouldReduce = useReducedMotion()

  if (shouldReduce) {
    return <div className={props.className}>{props.children}</div>
  }

  return (
    <motion.div
      initial={MOTION_VARIANTS.pageEnter.initial}
      animate={MOTION_VARIANTS.pageEnter.animate}
      transition={MOTION_TRANSITION.default}
      className={props.className}
    >
      {props.children}
    </motion.div>
  )
}

export function AnimatedOutlet() {
  const shouldReduce = useReducedMotion()
  // Key the page transition by the matched route id, not the resolved pathname.
  // Navigating between params of the same route (e.g. dashboard tabs served by
  // /dashboard/$section) then re-renders in place instead of remounting the
  // route component and discarding its state (such as the selected time range).
  const routeKey = useRouterState({
    select: (s) => s.matches.at(-1)?.routeId ?? s.location.pathname,
  })

  if (shouldReduce) {
    return (
      <div className='flex min-h-0 flex-1 flex-col'>
        <Outlet />
      </div>
    )
  }

  return (
    <motion.div
      key={routeKey}
      initial={MOTION_VARIANTS.pageEnter.initial}
      animate={MOTION_VARIANTS.pageEnter.animate}
      transition={MOTION_TRANSITION.fast}
      className='flex min-h-0 flex-1 flex-col'
    >
      <Outlet />
    </motion.div>
  )
}

interface StaggerContainerProps {
  children: ReactNode
  className?: string
  variants?: Variants
}

export function StaggerContainer(props: StaggerContainerProps) {
  const shouldReduce = useReducedMotion()

  if (shouldReduce) {
    return <div className={props.className}>{props.children}</div>
  }

  return (
    <motion.div
      variants={props.variants ?? STAGGER_VARIANTS}
      initial='initial'
      animate='animate'
      className={props.className}
    >
      {props.children}
    </motion.div>
  )
}

interface StaggerItemProps {
  children: ReactNode
  className?: string
  variants?: Variants
}

export function StaggerItem(props: StaggerItemProps) {
  return (
    <motion.div
      variants={props.variants ?? STAGGER_ITEM_VARIANTS}
      className={props.className}
    >
      {props.children}
    </motion.div>
  )
}

export function TableStaggerContainer(props: StaggerContainerProps) {
  const shouldReduce = useReducedMotion()

  if (shouldReduce) {
    return <>{props.children}</>
  }

  return (
    <motion.tbody
      variants={TABLE_STAGGER_VARIANTS}
      initial='initial'
      animate='animate'
      className={props.className}
    >
      {props.children}
    </motion.tbody>
  )
}

export function TableStaggerRow(props: StaggerItemProps) {
  return (
    <motion.tr variants={TABLE_ROW_VARIANTS} className={props.className}>
      {props.children}
    </motion.tr>
  )
}

export function CardStaggerContainer(props: StaggerContainerProps) {
  const shouldReduce = useReducedMotion()

  if (shouldReduce) {
    return <div className={props.className}>{props.children}</div>
  }

  return (
    <motion.div
      variants={CARD_STAGGER_VARIANTS}
      initial='initial'
      animate='animate'
      className={props.className}
    >
      {props.children}
    </motion.div>
  )
}

export function CardStaggerItem(props: StaggerItemProps) {
  return (
    <motion.div variants={CARD_ITEM_VARIANTS} className={props.className}>
      {props.children}
    </motion.div>
  )
}

interface FadeInProps {
  children: ReactNode
  className?: string
  delay?: number
}

export function FadeIn(props: FadeInProps) {
  const shouldReduce = useReducedMotion()

  if (shouldReduce) {
    return <div className={props.className}>{props.children}</div>
  }

  return (
    <motion.div
      initial={MOTION_VARIANTS.fadeIn.initial}
      animate={MOTION_VARIANTS.fadeIn.animate}
      transition={{
        ...MOTION_TRANSITION.default,
        delay: props.delay,
      }}
      className={props.className}
    >
      {props.children}
    </motion.div>
  )
}
