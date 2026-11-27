import type { ReactElement } from 'react';
import { Navigate, useLocation } from 'react-router-dom';
import { useAuthStore } from '@/stores';
import type { LoginRedirectState } from './loginRedirect';

/**
 * 未通过本次会话验证时一律交给登录页：
 * 保存了密码的用户在登录页经由启动画面自动登录，并以统一的"开灯"过渡回到原目标页；
 * 没有可恢复会话的用户直接看到登录表单。
 */
export function ProtectedRoute({ children }: { children: ReactElement }) {
  const location = useLocation();
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);

  if (!isAuthenticated) {
    const state: LoginRedirectState = {
      from: { pathname: location.pathname, search: location.search },
    };
    return <Navigate to="/login" replace state={state} />;
  }

  return children;
}
