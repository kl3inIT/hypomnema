import { Link, useNavigate } from "@tanstack/react-router"
import { HypomnemaIcon } from "@/components/icons/brand-icons"
import {
  ArrowLeft,
  AudioLines,
  BookOpen,
  ChevronLeft,
  ChevronRight,
  Box,
  ChartColumn,
  FolderGit2,
  Globe,
  ImageIcon,
  KeyRound,
  KeySquare,
  MessagesSquare,
  Mic,
  Plug,
  UserRound,
  type LucideIcon,
} from "lucide-react"
import type { ReactNode } from "react"

import { PageHeader, PageHeading } from "@/components/page-header"
import { appPage } from "@/lib/back-from-settings"
import { cn } from "@/lib/utils"

// Settings is what the owner sets up once, kept apart from the places of daily work. The references whose sidebar
// lists sessions put setup behind one entry at its foot and, inside, sections under headings one level deep, a
// route a section (docs/research/2026-11-10-menus-and-settings.md): the headings are t3code's
// (components/settings/settingsSearch.ts SETTINGS_NAV_GROUPS), and the sidebar's body gives way to the sections
// with a way back as Omnigent's does (web/src/shell/settingsNav.tsx). A section appears here once it has a page.
type Section = {
  to:
    | "/settings/models"
    | "/settings/web-search"
    | "/settings/images"
    | "/settings/audio"
    | "/settings/meetings"
    | "/settings/gateway-keys"
    | "/settings/usage"
    | "/settings/repositories"
    | "/settings/integrations"
    | "/settings/channels"
    | "/settings/vault"
    | "/settings/skills"
    | "/settings/about-you"
  label: string
  icon: LucideIcon
}

// The group "Gateway" holds the model gateway's models and keys (issue #199), as LiteLLM's sidebar heading "AI
// Gateway" and Coder's "AI Gateway keys": called "Models" until the owner renamed it, so that no item reads like its
// group (issue #293, 2026-11-13), and its web search (issue #354), images (issue #431) and audio (issue #694), with the model that writes meetings' notes beside the audio that hears
// them (issue #721), as
// OmniRoute and 9router keep their search and media providers beside their models; not "Agents", which the app's sidebar already has (2026-11-12). "Connections" holds
// what reaches outside, as OpenClaw's group of that name holds its Channels and t3code's its Secrets. The group "Agents" holds what is given to the agents' CLIs:
// their skills (issue #21; prototype the owner accepted, 2026-11-12), and the memory about the owner every agent
// gets, as OpenClaw keeps Memory beside Skills in its group of agents; and the repositories agents work in, as
// t3code's Settings lists its Projects (apps/web/src/components/settings/settingsSearch.ts) and Orca's its
// repositories (src/renderer/src/components/settings/navigation/SettingsSidebar.tsx), in this group by the owner's
// choice (issue #647, 2026-11-16).
const groups: { heading: string; sections: Section[] }[] = [
  {
    heading: "Gateway",
    sections: [
      { to: "/settings/models", label: "Models", icon: Box },
      { to: "/settings/web-search", label: "Web search", icon: Globe },
      { to: "/settings/images", label: "Images", icon: ImageIcon },
      { to: "/settings/audio", label: "Audio", icon: AudioLines },
      { to: "/settings/meetings", label: "Meetings", icon: Mic },
      { to: "/settings/gateway-keys", label: "Gateway keys", icon: KeySquare },
      { to: "/settings/usage", label: "Usage", icon: ChartColumn },
    ],
  },
  {
    heading: "Agents",
    sections: [
      { to: "/settings/skills", label: "Skills", icon: BookOpen },
      { to: "/settings/about-you", label: "About you", icon: UserRound },
      { to: "/settings/repositories", label: "Repositories", icon: FolderGit2 },
    ],
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

const rowClass =
  "flex h-8 items-center gap-2 rounded-lg px-2.5 text-sm font-medium text-muted-foreground/80 hover:bg-sidebar-row-hover hover:text-foreground [&.active]:bg-sidebar-row-active [&.active]:text-foreground"

// SettingsSidebar stands where the app's sidebar stands while the owner is in Settings. "Back to app" returns to the
// page the owner came from (lib/back-from-settings.ts, issue #747), else home.
export function SettingsSidebar() {
  const navigate = useNavigate()
  const back = appPage()

  return (
    <aside className="hidden h-full w-64 shrink-0 flex-col border-r bg-sidebar text-sidebar-foreground md:flex">
      <div className="flex h-(--workspace-topbar-height) shrink-0 items-center gap-2 px-3.5">
        <HypomnemaIcon className="size-6 shrink-0" />
        <span className="text-sm font-semibold tracking-tight">Settings</span>
      </div>
      <nav
        aria-label="Settings"
        className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-2"
      >
        <a
          href={back}
          className={rowClass}
          onClick={(e) => {
            // A plain click stays in the app; a click asking for a new tab or window gets one.
            if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
            e.preventDefault()
            void navigate({ href: back })
          }}
        >
          <ArrowLeft className="size-4" />
          Back to app
        </a>
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

// SettingsList is /settings on a phone, where the sidebar hides: the sections under the sidebar's headings, a row a
// section with a chevron, each opening its page, as OpenMausBot's narrow settings (Apache License 2.0,
// https://github.com/milind-soni/OpenMausBot; src/components/settings/SettingsNav.tsx, the narrow rows) and
// OpenHands' SettingsMobileHub (MIT, Copyright (c) 2025 OpenHands contributors, https://github.com/OpenHands/OpenHands;
// frontend/src/routes/settings-index.tsx). A desktop opens a section instead (issue #604).
export function SettingsList() {
  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-8">
        <nav aria-label="Settings" className="mx-auto flex w-full max-w-3xl flex-col gap-4 pt-2">
          <h1 className="px-2 text-2xl leading-tight font-semibold text-foreground">Settings</h1>
          {groups.map((group) => (
            <section
              key={group.heading}
              aria-label={group.heading}
              className="flex flex-col gap-0.5"
            >
              <h2 className="px-2 pb-1 text-xs font-medium text-muted-foreground">
                {group.heading}
              </h2>
              {group.sections.map((section) => (
                <Link
                  key={section.to}
                  to={section.to}
                  className="flex h-11 items-center gap-3 rounded-lg px-2 text-sm text-foreground hover:bg-sidebar-row-hover"
                >
                  <section.icon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                  <span className="min-w-0 flex-1 truncate">{section.label}</span>
                  <ChevronRight className="size-4 shrink-0 text-muted-foreground" aria-hidden />
                </Link>
              ))}
            </section>
          ))}
        </nav>
      </div>
    </main>
  )
}

// SettingsPage is one section's page, headed by the list pages' PageHeading. On a phone, where the sidebar hides,
// the header leads back to the list of sections, "‹ Settings", as OpenMausBot's narrow settings page
// (src/components/settings/SettingsWindow.tsx, data-settings-back).
export function SettingsPage({
  title,
  description,
  actions,
  wide,
  children,
}: {
  title: string
  // description is said under the title; a page that leads with something else says it itself.
  description?: string
  actions?: ReactNode
  // wide gives the page the room a table needs.
  wide?: boolean
  children: ReactNode
}) {
  return (
    <main className="flex min-w-0 flex-1 flex-col">
      <PageHeader
        back={
          <Link
            to="/settings"
            className="flex h-8 items-center gap-0.5 rounded-lg pe-2 text-sm font-medium text-muted-foreground hover:bg-accent hover:text-foreground"
          >
            <ChevronLeft className="size-4" aria-hidden />
            Settings
          </Link>
        }
      />
      <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-8 sm:px-5">
        <div
          className={cn(
            "mx-auto flex w-full min-w-0 flex-col gap-5 pt-2",
            wide ? "max-w-5xl" : "max-w-3xl",
          )}
        >
          <PageHeading title={title} description={description}>
            {actions}
          </PageHeading>
          {children}
        </div>
      </div>
    </main>
  )
}

// SettingsSection is a group of a page under a heading with its icon and a line of what it is for, as the Models page
// has its subscriptions, gateway models and API providers.
export function SettingsSection({
  id,
  icon: Icon,
  title,
  note,
  children,
}: {
  id: string
  icon: LucideIcon
  title: string
  note: string
  children: ReactNode
}) {
  return (
    <section aria-labelledby={id} className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <h2 id={id} className="flex items-center gap-2 text-sm font-semibold text-foreground">
          <Icon className="size-4 text-muted-foreground" aria-hidden />
          {title}
        </h2>
        <p className="text-xs text-muted-foreground">{note}</p>
      </div>
      {children}
    </section>
  )
}
