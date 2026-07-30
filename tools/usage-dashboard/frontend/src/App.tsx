import { Suspense } from "react";
import { DetailDrawer } from "./components/DetailDrawer";
import { Outlet } from "react-router-dom";
import { Header } from "./components/Header";

export default function App() {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <Header />
      <DetailDrawer />
      <main className="p-4">
        <Suspense fallback={<div>加载中…</div>}>
          <Outlet />
        </Suspense>
      </main>
    </div>
  );
}