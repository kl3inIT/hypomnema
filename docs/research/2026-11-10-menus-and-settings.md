# Menus and settings in the references (2026-11-10)

Read to decide where the web client puts what is set up once: git host integrations, repositories, machines,
models and accounts, profiles, access tokens. The owner asked how the others organize their menus before a
Settings page is built (issue #44).

Revisions read: t3code `b8c2cfad`, Orca `4c13ae46`, Omnigent `cf1021e`, Omnara `0ef1711`, OpenMausBot `1de1164`,
OpenClaw `d622a2ba4`, Coder `b798061`, and the clones of Companion, Buzz, LiteLLM, Bifrost, Hermes, Infisical and
Helicone as they are in `.tmp/`. Two agents read the clones. The groups of t3code, Orca, OpenClaw and Omnara
quoted below were checked against the files afterwards; the rest is as the agents reported it.

## Apps that drive agents

| Project | Main sidebar | Settings is entered from | Settings is | Sections |
|---|---|---|---|---|
| t3code | Threads by state; a footer row of icons: Settings, Pull requests, Usage | The footer icon | A large modal with its own left navigation, a route a section (`routes/_chat.settings.*`) | Groups with headings, one level (`components/settings/settingsSearch.ts` `SETTINGS_NAV_GROUPS`): no heading (General, Appearance, Chat, …), Agents (Providers, Threads, Browser, Devices, Scheduled tasks), Code (Projects, Source control, Worktrees), Connections (Environments, Sharing & access, Secrets), no heading (Maintenance, Labs, About, Archive) |
| Orca | Search, Tasks, a few pages, the projects and worktrees; a footer gear | The footer gear | The whole window, with "Back to app", a search and one long page | Groups with headings (`components/settings/navigation/settings-navigation-foundations.ts`): AI Capabilities, Set Up (with Integrations), Workflows (with Git & Source Control, Task Sources), Interface, Remote Hosts, Privacy & Security, Advanced, Experimental, and one entry a project |
| Omnigent | New session, Automations, Inbox, Canvas, Usage; the sessions; a gear in the header | The header gear | The sidebar's own body swapped for the settings navigation, with "Back"; a route a section | General, Customize, Desktop, Admin, Archived (`shell/settingsNav.tsx`) |
| OpenMausBot | The bots and chats; Tools; an account menu at the foot | The account menu | A modal; a simple mode with flat pages and an advanced one with groups | You, AI (Model providers, API keys, Skills), Devices (Servers, Logins), Account |
| Buzz | Inbox, Agents, the channels; a profile card at the foot | The profile card | The whole shell, with "Back to app" | Personal, Communities, App (Agents, Compute, …) |
| Omnara | Everything is in the main sidebar, by organization or project: Overview, Agents, Integrations, Usage; Resources (Models, Machines, Secrets, Skills, Memory); Access (Members, API Tokens) (`components/app-shell/SidebarNav.tsx`) | The organization's and the user's menus | One page each; no settings shell | — |
| Companion | The conversations; Applications and Account at the foot | — | Standalone pages | — |

## Control planes and admin consoles

| Project | Where setup lives | Sections |
|---|---|---|
| OpenClaw | "Settings" in the profile menu takes over the sidebar, with Back and a search (`ui/src/app-navigation.ts` `SETTINGS_NAVIGATION_GROUPS`) | No heading (Profile, Appearance, Notifications); This computer; Connections (Gateway, Channels, Devices, Cloud environments); Agents & Tools (Agents, Models, Plugins, Skills, MCP, Memory, Automation); Privacy & security (Secrets, Approvals); System. Its comment: sessions, worktrees and activity "are workspace destinations, not settings" |
| Coder | A top bar; "Admin settings" opens areas that each have a flat sidebar | Deployment (…, External Authentication, …); AI (Spend, AI Gateway keys, Providers, Models, MCP servers, …); the user's own (Tokens, Secrets, …) |
| Helicone | "Configure" at the sidebar's foot opens a page with its own list | Organization; Developer (API Keys, Providers, Webhooks, Connections); Accounts |
| Infisical | "Settings" swaps the sidebar for a submenu with a way back; a server console apart | General (Home, Integrations), Administration (Access Control, …, Settings) |
| LiteLLM | One sidebar under headings, Settings as its last group | AI Gateway, Observability, Access Control, Developer Tools, Settings |
| Bifrost | One flat sidebar of parents that open; Settings last | Models, MCP Servers, Agents, Governance, Cluster, …, Settings |
| Hermes | One flat sidebar; Config and Keys are entries like the rest | — |

## Where each thing is put

| Thing | In the references |
|---|---|
| Git hosts and their tokens | Orca: Set Up → Integrations, cards under "Review providers". t3code: Code → Source control, accounts and tokens a host. Coder: External Authentication. Omnigent: Sandbox Integrations |
| Task trackers | Orca: Integrations, "Task providers" (Linear, Jira), and Task Sources |
| Repositories | t3code: Code → Projects. Orca: a group "Projects" with an entry a repository |
| Worktrees and their cleanup | t3code: Code → Worktrees (the rules) and Maintenance (delete now). Orca: a cleanup dialog. OpenClaw: not a setting |
| Machines | Omnara: Resources → Machines. t3code: Connections → Environments. OpenClaw: Connections → Devices, Cloud environments. Orca: Remote Hosts. Bifrost: Cluster → Nodes |
| Model providers, accounts and keys | t3code: Agents → Providers. Orca: AI Capabilities → AI Provider Accounts. OpenClaw: Agents & Tools → Models. Coder: AI → Providers, Models. Omnara: Resources → Models. Helicone: Developer → Providers |
| Profiles of agents | Omnigent: Harnesses, Custom agents. OpenClaw: Agents & Tools → Agents. Omnara: Agents |
| The product's own tokens | t3code: Connections → Sharing & access. Omnara: Access → API Tokens. Coder: the user's Tokens. Helicone: Developer → API Keys |
| Secrets | t3code: Connections → Secrets. OpenClaw: Privacy & security → Secrets. Omnara: Resources → Secrets |
| Work on a schedule | t3code: Agents → Scheduled tasks. Orca: Workflows → Automations. Omnigent: Automations in the main sidebar |

## What they share, and where they part

- An app whose sidebar is a list of sessions keeps that sidebar for work and puts setup behind one entry at its
  foot (t3code, Orca, Omnigent, OpenMausBot, Buzz, OpenClaw). Omnara, which lists no sessions in its sidebar, puts
  every resource in it under headings.
- Settings then replaces the sidebar or the window, with a way back (Orca, Omnigent, OpenClaw, Buzz), or is a
  modal with its own navigation (t3code, OpenMausBot).
- Sections are grouped under headings, one level deep, each a route (t3code, Orca, Omnigent, OpenClaw, Buzz,
  Helicone). The products that nest two levels are the admin consoles (LiteLLM, Bifrost, Coder's AI).
- The larger trees have a search over settings (t3code, Orca, OpenMausBot, OpenClaw).
- One integration is a card with a status (Orca, Hermes, Helicone) or a row of a table (Coder, Infisical); adding
  one is a dialog.
- On a narrow screen the navigation becomes a drawer, an icon rail (t3code) or a select (OpenMausBot).

## For Hypomnema

Not decided until the owner chooses.

- The sidebar holds the chats, so setup goes behind one "Settings" entry at its foot, which swaps the sidebar's
  body for the settings sections with a way back, a route a section, as Omnigent and OpenClaw.
- Sections under headings taken from t3code and OpenClaw: Agents (Models, Profiles), Code (Repositories),
  Connections (Integrations, Machines, Access tokens, later Secrets). Only sections that have a page are shown.
- Git hosts are cards in Integrations, as Orca's review providers; a tracker such as Linear joins them there.

Inferred, with no reference: which heading each of Hypomnema's sections goes under.
