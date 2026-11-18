import type { Location } from 'react-router-dom';

/** 受保护路由跳往登录页时携带的原目标位置（路径与查询参数） */
export type LoginRedirectState = { from?: Pick<Location, 'pathname' | 'search'> };

/** 登录完成后回到进入登录页之前的目标页，默认仪表盘；不会回到登录页自身 */
export function resolveLoginRedirect(state: unknown): string {
  const from = (state as LoginRedirectState | null)?.from;
  const pathname = from?.pathname;
  if (!pathname || pathname === '/login') return '/';
  return `${pathname}${from?.search ?? ''}`;
}
