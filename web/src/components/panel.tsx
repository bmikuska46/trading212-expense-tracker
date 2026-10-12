import type { ReactNode } from "react";
import { CardAction, CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";

// PanelHeader is the compact console card header: a small title, an optional
// mono count beside it and an action on the right, above a hairline.
export function PanelHeader({ id, title, count, action, className }: {
  id?: string; title: ReactNode; count?: ReactNode; action?: ReactNode; className?: string;
}) {
  return <CardHeader className={cn("min-h-11 items-center border-b px-3.5 py-2 [.border-b]:pb-2", className)}>
    <CardTitle>
      <h2 id={id} className="flex items-center gap-2 text-[0.8125rem] font-medium">
        {title}
        {count !== undefined && <span className="font-mono text-[0.6875rem] font-medium text-faint">{count}</span>}
      </h2>
    </CardTitle>
    {action && <CardAction className="row-span-1 flex items-center gap-2 self-center text-xs text-muted-foreground">{action}</CardAction>}
  </CardHeader>;
}

// Table heads and cells in panels share the dense console rhythm: mono
// uppercase labels and a wider inset at the panel edges.
export const headClass = "h-8 px-2 font-mono text-[0.6875rem] font-medium tracking-wider text-faint uppercase first:pl-3.5 last:pr-3.5";
export const cellClass = "px-2 py-2 first:pl-3.5 last:pr-3.5";

// Panel footers carry a short faint note under a hairline.
export const footerClass = "border-t bg-transparent px-3.5 py-2.5 text-xs text-faint";

// Alerts inside pages are compact strips like the notifications, so a page
// reads top to bottom without large gaps.
export const alertClass = "px-3 py-2.5 text-[0.8125rem]";
export const alertDescriptionClass = "text-[0.8125rem] [&_p:not(:last-child)]:mb-1.5";

// Long forms use small labels and descriptions; legends match table heads.
export const compactFields = "[&_[data-slot=field]]:gap-1 [&_[data-slot=field-label]]:text-xs [&_[data-slot=field-label]]:text-muted-foreground [&_[data-slot=field-description]]:text-xs [&_[data-slot=field-error]]:text-xs";
export const legendClass = "font-mono text-[0.6875rem] font-medium tracking-wider text-faint uppercase";
