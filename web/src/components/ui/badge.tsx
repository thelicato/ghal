// Adapted from shadcn/ui (MIT). See THIRD_PARTY_NOTICES.md.
import type { ComponentProps } from "react"
import { cn } from "@/lib/utils"

export function Badge({ className, ...props }: ComponentProps<"span">) {
  return <span data-slot="badge" className={cn("inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium whitespace-nowrap", className)} {...props} />
}
