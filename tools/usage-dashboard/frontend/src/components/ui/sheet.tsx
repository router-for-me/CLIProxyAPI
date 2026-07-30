"use client";

import * as React from "react";
import { Drawer as BaseDrawer } from "@base-ui/react/drawer";
import { cn } from "@/lib/utils";
import { X } from "lucide-react";

/* ------------------------------------------------------------------------- */
/* Sheet Root — wraps Drawer.Root with right-side swipe direction           */
/* ------------------------------------------------------------------------- */

function Sheet({
  open,
  onOpenChange,
  children,
}: {
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
  children?: React.ReactNode;
}) {
  return (
    <BaseDrawer.Root
      open={open}
      onOpenChange={onOpenChange}
      swipeDirection="right"
      modal
    >
      <BaseDrawer.Backdrop className="fixed inset-0 bg-black/50 data-[starting-style]:opacity-0 data-[ending-style]:opacity-0 transition-opacity duration-300" />
      {children}
    </BaseDrawer.Root>
  );
}

/* ------------------------------------------------------------------------- */
/* SheetContent — portal + popup + content wrapper                          */
/* ------------------------------------------------------------------------- */

function SheetContent({
  children,
  className,
}: {
  children?: React.ReactNode;
  className?: string;
}) {
  return (
    <BaseDrawer.Portal>
      <BaseDrawer.Viewport className="fixed right-0 top-0 z-50 h-full">
        <BaseDrawer.Popup
          className={cn(
            "h-full w-full max-w-md border-l border-border bg-background shadow-lg outline-none",
            "transition-transform duration-300 ease-in-out",
            "data-[starting-style]:translate-x-full data-[ending-style]:translate-x-full",
            "data-[swipe-direction=right]:translate-x(calc(var(--drawer-swipe-movement-x,0px)))",
            className,
          )}
        >
          <BaseDrawer.Content className="flex h-full flex-col overflow-y-auto p-6">
            {children}
          </BaseDrawer.Content>
        </BaseDrawer.Popup>
      </BaseDrawer.Viewport>
    </BaseDrawer.Portal>
  );
}

/* ------------------------------------------------------------------------- */
/* SheetHeader — styled header section                                        */
/* ------------------------------------------------------------------------- */

function SheetHeader({
  children,
  className,
}: {
  children?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("mb-6 flex items-center justify-between", className)}>
      <div className="flex flex-col gap-1">{children}</div>
      <BaseDrawer.Close className="rounded-md p-1 text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <X className="h-4 w-4" />
        <span className="sr-only">Close</span>
      </BaseDrawer.Close>
    </div>
  );
}

/* ------------------------------------------------------------------------- */
/* SheetTitle — wraps Drawer.Title                                           */
/* ------------------------------------------------------------------------- */

function SheetTitle({
  children,
  className,
}: {
  children?: React.ReactNode;
  className?: string;
}) {
  return (
    <BaseDrawer.Title className={cn("text-lg font-semibold text-foreground", className)}>
      {children}
    </BaseDrawer.Title>
  );
}

export { Sheet, SheetContent, SheetHeader, SheetTitle };