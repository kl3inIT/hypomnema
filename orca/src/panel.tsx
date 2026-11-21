// What waits for the owner, one card per item, after the web's Work page and its question form
// (web/src/features/questions/question-form.tsx): a question's options as buttons and a text box where an option or
// a question without options takes text; the first answer wins on the server, so an answer someone else gave first
// is said as such. "Open" shows the pull request a stalled review is about, else the Task on the web.
import {
  atom,
  Badge,
  Button,
  Callout,
  Card,
  EmptyState,
  Heading,
  Input,
  plugin,
  read,
  Row,
  set,
  shell,
  Spinner,
  Stack,
  Text,
  Textarea,
  update,
  type PanelContext
} from 'orca'
import type { InboxState } from './protocol'
import type { Item } from './waiting'

const inbox = atom<InboxState | null>(null)
const editing = atom(false)
const server = atom('')
const token = atom('')
const connectError = atom<string | undefined>(undefined)
// The text typed for each question, and what came of its last answer, by question id.
const drafts = atom<Record<string, string>>({})
type Outcome = { tone: 'danger' | 'warning'; text: string }
const outcomes = atom<Record<string, Outcome>>({})
const sending = atom<string | undefined>(undefined)

export function setup(ctx: PanelContext): void {
  ctx.subscriptions.push(plugin.on('inbox.changed', (state) => set(inbox, state)))
  void plugin
    .request('inbox.get')
    .then((state) => {
      set(inbox, state)
      set(server, state.server)
    })
    .catch((e: unknown) => set(connectError, messageOf(e)))
}

async function connect(): Promise<void> {
  set(connectError, undefined)
  try {
    const state = await plugin.request('inbox.connect', {
      server: read(server),
      token: read(token)
    })
    // The token is kept in Orca's secrets and never shown again.
    set(token, '')
    set(inbox, state)
    set(editing, false)
  } catch (e) {
    set(connectError, messageOf(e))
  }
}

async function answer(item: Item, option?: number): Promise<void> {
  const q = item.question
  if (!q || read(sending)) return

  const takesText = option === undefined || !!q.options[option]?.allows_text
  const text = takesText ? (read(drafts)[q.id] ?? '').trim() : ''

  set(sending, q.id)
  try {
    // A request carries JSON only: a field without a value is left out, not sent as undefined.
    const result = await plugin.request('inbox.answer', {
      questionId: q.id,
      ...(option === undefined ? {} : { option }),
      ...(text ? { text } : {})
    })
    if (result.outcome === 'answered') {
      update(outcomes, ({ [q.id]: _, ...rest }) => rest)
    } else if (result.outcome === 'taken') {
      note(q.id, { tone: 'warning', text: `Already answered: ${result.message}` })
    } else {
      note(q.id, { tone: 'danger', text: `Not sent: ${result.message}` })
    }
  } catch (e) {
    note(q.id, { tone: 'danger', text: `Not sent: ${messageOf(e)}` })
  } finally {
    set(sending, undefined)
  }
}

function note(id: string, outcome: Outcome): void {
  update(outcomes, (all) => ({ ...all, [id]: outcome }))
}

function open(item: Item, base: string): void {
  void shell.openExternal(item.link ?? `${base}/team?task=${encodeURIComponent(item.taskId)}`)
}

function Connect({ state }: { state: InboxState }) {
  return (
    <Stack gap="md">
      <Heading>Connect to Hypomnema</Heading>
      <Text muted size="sm">
        Mint an owner token for Orca on the control plane with: hypomnema tokens add orca --kind
        owner
      </Text>
      <Input
        label="Server"
        placeholder="https://hypomnema.example.com"
        value={read(server)}
        onChange={(v) => set(server, v)}
      />
      <Input
        label={state.hasToken ? 'Owner token (leave empty to keep the one kept)' : 'Owner token'}
        type="password"
        value={read(token)}
        onChange={(v) => set(token, v)}
        onSubmit={() => void connect()}
      />
      {read(connectError) ? <Text tone="danger">{read(connectError)}</Text> : null}
      <Row gap="xs">
        <Button
          onPress={connect}
          disabled={!read(server).trim() || (!state.hasToken && !read(token).trim())}
        >
          Connect
        </Button>
        {state.status !== 'unconfigured' ? (
          <Button variant="ghost" onPress={() => set(editing, false)}>
            Cancel
          </Button>
        ) : null}
      </Row>
    </Stack>
  )
}

function ItemCard({ item, base }: { item: Item; base: string }) {
  const q = item.question
  const outcome = q ? read(outcomes)[q.id] : undefined
  const takesText = !!q && (q.options.length === 0 || q.options.some((o) => o.allows_text))
  const busy = !!q && read(sending) === q.id

  return (
    <Card title={item.run} description={item.task}>
      <Stack gap="sm">
        <Text>{item.text}</Text>
        {q && takesText ? (
          <Textarea
            label={q.options.length ? 'Details' : 'Answer'}
            rows={2}
            value={read(drafts)[q.id] ?? ''}
            onChange={(v) => update(drafts, (all) => ({ ...all, [q.id]: v }))}
          />
        ) : null}
        {outcome ? <Text tone={outcome.tone}>{outcome.text}</Text> : null}
        <Row gap="xs" wrap>
          {q
            ? q.options.length
              ? q.options.map((o, i) => (
                  <Button key={o.label} size="sm" disabled={busy} onPress={() => answer(item, i)}>
                    {o.label}
                  </Button>
                ))
              : [
                  <Button key="send" size="sm" disabled={busy} onPress={() => answer(item)}>
                    Send
                  </Button>
                ]
            : null}
          <Button size="sm" variant="ghost" icon="open-external" onPress={() => open(item, base)}>
            Open
          </Button>
        </Row>
      </Stack>
    </Card>
  )
}

export default function Panel() {
  const state = read(inbox)
  if (!state) return <Spinner label="Loading" />
  if (state.status === 'unconfigured' || read(editing)) return <Connect state={state} />

  const n = state.items.length

  return (
    <Stack gap="md">
      <Row justify="between">
        <Heading>Waiting for you</Heading>
        <Badge tone={n > 0 ? 'warning' : 'neutral'}>{String(n)}</Badge>
      </Row>
      {state.status === 'error' ? (
        <Callout tone="danger" title="The control plane cannot be reached">
          {state.error ?? ''}
        </Callout>
      ) : state.status === 'connecting' ? (
        <Text muted size="xs">
          Connecting to {state.server}
        </Text>
      ) : null}
      {n === 0 ? (
        // Nothing is known to wait until the control plane has answered.
        state.status === 'live' ? (
          <EmptyState icon="check" title="Nothing waits for you" />
        ) : null
      ) : (
        state.items.map((item) => <ItemCard key={item.key} item={item} base={state.server} />)
      )}
      <Row>
        <Button variant="ghost" size="sm" onPress={() => set(editing, true)}>
          Change server or token
        </Button>
      </Row>
    </Stack>
  )
}

function messageOf(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}
