import { Link } from "@tanstack/react-router"
import { ArrowLeft, Box, KeyRound, MessagesSquare, Plug, type LucideIcon } from "lucide-react"
import type { ReactNode } from "react"

import { PageHeader } from "@/features/shell/app-shell"
import { cn } from "@/lib/utils"

// Settings is what the owner sets up once, kept apart from the places of daily work. The references whose sidebar
// lists sessions put setup behind one entry at its foot and, inside, sections under headings one level deep, a
// route a section (docs/research/2026-11-10-menus-and-settings.md): the headings are t3code's
// (components/settings/settingsSearch.ts SETTINGS_NAV_GROUPS), and the sidebar's body gives way to the sections
// with a way back as Omnigent's does (web/src/shell/settingsNav.tsx). A section appears here once it has a page.
// Repositories has a page (/settings/repositories) but no entry: what a registered repository is, and whether it
// belongs here or in the app's sidebar as a project, is being decided (issue #44, owner, 2026-11-10).
type Section = {
  to:
    | "/settings/models"
    | "/settings/repositories"
    | "/settings/integrations"
    | "/settings/channels"
    | "/settings/vault"
  label: string
  icon: LucideIcon
}

// The group "Models" holds what the model gateway serves from (issue #199): the owner named it so, not "Agents",
// which the app's sidebar already has (2026-11-12).
const groups: { heading: string; sections: Section[] }[] = [
  {
    heading: "Models",
    sections: [{ to: "/settings/models", label: "Models", icon: Box }],
  },
  {
    heading: "Connections",
    sections: [
      { to: "/settings/integrations", label: "Integrations", icon: Plug },
      { to: "/settings/channels", label: "Channels", icon: MessagesSquare },
      { to: "/settings/vault", label: "Vault", icon: KeyRound },
    ],
  },
]

const sections = groups.flatMap((group) => group.sections)

const rowClass =
  "flex h-8 items-center gap-2 rounded-lg px-2.5 text-sm font-medium text-muted-foreground/80 hover:bg-sidebar-row-hover hover:text-foreground [&.active]:bg-sidebar-row-active [&.active]:text-foreground"

// SettingsSidebar stands where the app's sidebar stands while the owner is in Settings.
export function SettingsSidebar() {
  return (
    <aside className="hidden h-full w-64 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground md:flex">
      <div className="flex h-(--workspace-topbar-height) shrink-0 items-center px-3.5">
        <span className="text-sm font-semibold tracking-tight">Settings</span>
      </div>
      <nav
        aria-label="Settings"
        className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-2"
      >
        <Link to="/" className={rowClass}>
          <ArrowLeft className="size-4" />
          Back to app
        </Link>
        {groups.map((group) => (
          <div key={group.heading} className="flex flex-col gap-px">
            <p className="px-2.5 pb-1 text-xs font-medium text-muted-foreground">{group.heading}</p>
            {group.sections.map((section) => (
              <Link key={section.to} to={section.to} className={rowClass}>
                <section.icon className="size-4" />
                {section.label}
              </Link>
            ))}
          </div>
        ))}
      </nav>
    </aside>
  )
}

// SettingsPage is one section's page. On a phone, where the sidebar hides, the sections are a row of links under
// the header.
export function SettingsPage({
  title,
  description,
  actions,
  wide,
  children,
}: {
  title: string
  description: string
  actions?: ReactNode
  // wide gives the page the room a table needs.
  wide?: boolean
  children: ReactNode
}) {
  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader title={title}>
        <div className="ms-auto flex items-center gap-2">{actions}</div>
      </PageHeader>
      {sections.length > 1 && (
        <nav
          aria-label="Settings sections"
          className="flex shrink-0 gap-1 overflow-x-auto px-3 pb-2 md:hidden"
        >
          {sections.map((section) => (
            <Link
              key={section.to}
              to={section.to}
              className={cn(
                "inline-flex h-8 shrink-0 items-center gap-1.5 rounded-lg px-2.5 text-sm text-muted-foreground",
                "[&.active]:bg-foreground/9 [&.active]:text-foreground",
              )}
            >
              <section.icon className="size-4" />
              {section.label}
            </Link>
          ))}
        </nav>
      )}
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-8 sm:px-5">
        <div
          className={cn(
            "mx-auto flex w-full min-w-0 flex-col gap-5 pt-2",
            wide ? "max-w-5xl" : "max-w-3xl",
          )}
        >
          <p className="text-sm text-muted-foreground">{description}</p>
          {children}
        </div>
      </div>
    </main>
  )
}
