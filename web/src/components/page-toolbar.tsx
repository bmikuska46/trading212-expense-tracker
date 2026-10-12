"use client";

import { createContext, useContext, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useIsMobile } from "@/hooks/use-mobile";

const ToolbarContext = createContext<HTMLElement | null>(null);

export const ToolbarSlot = ToolbarContext.Provider;

// PageToolbar places page controls such as the month picker in the top bar
// on wide screens. Phones have no room there, so the controls stay where the
// page renders them.
export function PageToolbar({ children }: { children: ReactNode }) {
  const slot = useContext(ToolbarContext);
  const mobile = useIsMobile();
  if (mobile || !slot) return <div className="flex items-center gap-2 md:hidden">{children}</div>;
  return createPortal(children, slot);
}
