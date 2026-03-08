/* ============================================================
 * 主布局容器
 *
 * 结构:
 * - 桌面端: 固定侧边栏(240px) + 右侧主区域(Navbar + Outlet)
 * - 移动端: 汉堡菜单触发侧边栏 Overlay 抽屉
 * 背景色: #F8FAFC
 * ============================================================ */

import { useState, useCallback, useEffect } from 'react'
import { Outlet, useLocation } from 'react-router-dom'
import Sidebar from './Sidebar'
import Navbar from './Navbar'

export default function MainLayout() {
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const location = useLocation()

  /* 路由变化时自动关闭移动端侧边栏 */
  useEffect(() => {
    setSidebarOpen(false)
  }, [location.pathname])

  const toggleSidebar = useCallback(() => setSidebarOpen((v) => !v), [])

  return (
    <div className="flex min-h-screen bg-[#F8FAFC]">
      {/* ---- 移动端遮罩 ---- */}
      {sidebarOpen && (
        <div
          className="fixed inset-0 z-40 bg-black/30 lg:hidden"
          onClick={() => setSidebarOpen(false)}
        />
      )}

      {/* ---- 侧边栏 ---- */}
      {/* 桌面: 固定显示 / 移动: 滑入抽屉 */}
      <div
        className={`
          fixed inset-y-0 left-0 z-50 w-[240px] transform transition-transform duration-200 ease-in-out
          lg:translate-x-0
          ${sidebarOpen ? 'translate-x-0' : '-translate-x-full'}
        `}
      >
        <Sidebar />
      </div>

      {/* ---- 主内容区 ---- */}
      <div className="flex flex-col flex-1 lg:ml-[240px]">
        <Navbar onToggleSidebar={toggleSidebar} />
        <main className="flex-1 p-4 sm:p-6 overflow-y-auto">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
