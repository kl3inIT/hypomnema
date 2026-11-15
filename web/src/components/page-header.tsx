import { Link } from "@tanstack/react-router"
import { Menu } from "lucide-react"
import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

// PageHeader is the quiet bar above a page: on a phone a way to the start page, the title, and the page's tools.
// MenuLink goes to the menu on a phone, where there is no sidebar. A desktop does not show it.
export function MenuLink({ className }: { className?: string }) {
  return (
    <Link
      to="/"
      aria-label="Menu"
      className={cn(
        "flex size-8 shrink-0 items-center justify-center rounded-lg text-muted-foreground hover:bg-accent hover:text-foreground md:hidden",
        className,
      )}
    >
      <Menu className="size-4" />
    </Link>
  )
}

// The bar carries a detail page's breadcrumb ("Agents / reviewer", t3code's SettingsBreadcrumb.tsx) or a page's
// tools; a list page, whose title is its PageHeading, gives it neither, and it shows only on a phone, for the menu.
export function PageHeader({ title, children }: { title?: ReactNode; children?: ReactNode }) {
  return (
    <header
      className={cn(
        "flex min-h-(--workspace-topbar-height) shrink-0 flex-wrap items-center gap-x-2 gap-y-2 bg-background px-3 py-2 sm:px-5",
        !title && !children && "md:hidden",
      )}
    >
      <MenuLink className="-ms-1.5" />
      {title && <h1 className="min-w-0 truncate text-sm font-medium text-foreground">{title}</h1>}
      {children}
    </header>
  )
}

// PageHeading is a list page's head: its title, one plain sentence under it saying what the page holds, the page's
// actions on the right and a rule below, as Orca's settings section (MIT, Copyright (c) 2026 Lovecast Inc.,
// https://github.com/stablyai/orca; components/settings/section-layout/SettingsSection.tsx) and Beszel's systems
// table (MIT, Copyright (c) 2024 henrygd, https://github.com/henrygd/beszel; components/systems-table/
// systems-table.tsx CardHead, ui/card.tsx). On a phone the actions wrap under the title, as Beszel's.
export function PageHeading({
  title,
  description,
  children,
  className,
}: {
  title: ReactNode
  description?: ReactNode
  children?: ReactNode
  className?: string
}) {
  // The description runs under the title and the actions alike, so a wide set of actions (a search box) does not
  // squeeze it into a narrow column.
  return (
    <div
      className={cn(
        "grid grid-cols-1 gap-x-4 gap-y-2 border-b border-border/60 pb-5 md:grid-cols-[minmax(0,1fr)_auto] md:pt-6",
        className,
      )}
    >
      <h1 className="self-center text-2xl leading-tight font-semibold text-foreground">{title}</h1>
      {children && (
        <div className="flex flex-wrap items-center gap-2 max-md:order-last md:justify-end">
          {children}
        </div>
      )}
      {description && (
        <p className="max-w-3xl text-sm leading-6 text-muted-foreground md:col-span-2">
          {description}
        </p>
      )}
    </div>
  )
}
