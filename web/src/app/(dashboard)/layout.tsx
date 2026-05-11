"use client";

import { useAuth } from "@/lib/auth-context";
import { SidebarLayout } from "@/components/sidebar-layout";
import { Spinner } from "@/components/ui/spinner";

export default function DashboardGroupLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const { isAuthenticated, isLoading } = useAuth();

  if (isLoading) {
    return (
      <div className="flex min-h-svh items-center justify-center">
        <Spinner className="size-6" />
      </div>
    );
  }

  if (!isAuthenticated) {
    return null;
  }

  return <SidebarLayout>{children}</SidebarLayout>;
}
