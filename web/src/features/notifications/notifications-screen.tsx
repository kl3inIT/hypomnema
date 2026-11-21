import { Bell, BellOff, BellRing, Send, Share, SquarePlus, Trash2 } from "lucide-react"
import { useState } from "react"

import type { PushSubscription } from "@/api/generated/types.gen"
import { ErrorAlert } from "@/components/error-alert"
import { SettingsPage } from "@/components/settings-page"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty"
import {
  Item,
  ItemActions,
  ItemContent,
  ItemDescription,
  ItemGroup,
  ItemTitle,
} from "@/components/ui/item"
import { Spinner } from "@/components/ui/spinner"
import { said } from "@/lib/said"

import { usePush } from "./use-push"

type Push = ReturnType<typeof usePush>

const dayFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" })

// NotificationsScreen is where the owner turns notifications on for the device in his hand and sees every device
// that gets them (issue #1047): a device he removes and one he sends a test to, as Coder's devices and test push.
// No switch per kind: everything pushed is something waiting on him (the owner's Second Bite has none either).
export function NotificationsScreen() {
  const push = usePush()

  return (
    <SettingsPage
      title="Notifications"
      description="Your devices are told when something waits on you: a question for you, an Approval, a Connection a Bot asks for, a to-do that needs you. They are told even when no Hypomnema tab is open."
    >
      <ThisDevice push={push} />
      <Devices push={push} />
    </SettingsPage>
  )
}

function ThisDevice({ push }: { push: Push }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>This device</CardTitle>
        <CardDescription>{deviceState(push)}</CardDescription>
        {push.here && (
          <CardAction>
            <Badge variant="secondary">
              <BellRing data-icon="inline-start" />
              On
            </Badge>
          </CardAction>
        )}
      </CardHeader>
      <CardContent>
        <div className="flex flex-col gap-3">
          {push.support === "install" && <InstallSteps />}
          {push.support === "supported" && push.permission !== "denied" && (
            <div className="flex flex-wrap gap-2">
              {push.here ? (
                <>
                  <TestButton push={push} device={push.here} />
                  <Button
                    variant="outline"
                    disabled={push.remove.isPending}
                    onClick={() =>
                      push.here &&
                      push.remove.mutate(push.here, {
                        onSuccess: () => said("Notifications turned off on this device"),
                      })
                    }
                  >
                    {push.remove.isPending ? <Spinner /> : <BellOff data-icon="inline-start" />}
                    Turn off
                  </Button>
                </>
              ) : (
                <TurnOnButton push={push} />
              )}
            </div>
          )}
          <ErrorAlert error={push.turnOn.error ?? push.remove.error} />
        </div>
      </CardContent>
    </Card>
  )
}

// deviceState says where this device stands, in a line.
function deviceState(push: Push) {
  if (push.support === "install") {
    return "On an iPhone or iPad, notifications come to Hypomnema opened from the Home Screen."
  }
  if (push.support === "unsupported") return "This browser cannot show notifications."
  if (push.permission === "denied") {
    return "Notifications are blocked for this site. Allow them in the browser's settings for this site, then come back."
  }
  if (push.here) return "This device is told when something waits on you."

  return "This device is not told yet."
}

// TurnOnButton asks the browser's permission, from the owner's press only (Second Bite, OpenClaw), and subscribes.
export function TurnOnButton({
  push,
  label = "Turn on notifications",
}: {
  push: Push
  label?: string
}) {
  return (
    <Button
      disabled={push.turnOn.isPending || !push.devices.data}
      onClick={() =>
        push.turnOn.mutate(undefined, {
          onSuccess: () => said("Notifications turned on on this device"),
        })
      }
    >
      {push.turnOn.isPending ? <Spinner /> : <Bell data-icon="inline-start" />}
      {label}
    </Button>
  )
}

function TestButton({ push, device }: { push: Push; device: PushSubscription }) {
  const busy = push.test.isPending && push.test.variables?.id === device.id

  return (
    <Button
      variant="outline"
      disabled={busy}
      onClick={() =>
        push.test.mutate(device, {
          onSuccess: () => said(`Test notification sent to ${name(device)}`),
        })
      }
    >
      {busy ? <Spinner /> : <Send data-icon="inline-start" />}
      Send a test
    </Button>
  )
}

// InstallSteps are the steps an iPhone needs before it can take a notification: Safari gives Web Push only to an
// app added to the Home Screen (iOS 16.4+), as the owner's Second Bite shows them (push-card.tsx:155-169) and
// OpenClaw's install-required state asks.
function InstallSteps() {
  return (
    <ol aria-label="Add Hypomnema to the Home Screen" className="flex flex-col gap-2 text-sm">
      <li className="flex items-start gap-2">
        <span className="font-medium text-muted-foreground tabular-nums">1.</span>
        <span>
          Tap <Share aria-label="Share" className="inline size-4 align-text-bottom" /> Share in
          Safari&apos;s toolbar.
        </span>
      </li>
      <li className="flex items-start gap-2">
        <span className="font-medium text-muted-foreground tabular-nums">2.</span>
        <span>
          Choose <SquarePlus aria-hidden className="inline size-4 align-text-bottom" /> Add to Home
          Screen, then Add.
        </span>
      </li>
      <li className="flex items-start gap-2">
        <span className="font-medium text-muted-foreground tabular-nums">3.</span>
        <span>
          Open Hypomnema from the Home Screen and turn notifications on in Settings, Notifications.
        </span>
      </li>
    </ol>
  )
}

const name = (device: PushSubscription) => device.label || "Unnamed device"

function Devices({ push }: { push: Push }) {
  const [removing, setRemoving] = useState<string | null>(null)
  const devices = push.devices

  return (
    <section aria-labelledby="devices-heading" className="flex flex-col gap-2">
      <h2 id="devices-heading" className="text-sm font-medium">
        Your devices
      </h2>
      {devices.isPending && (
        <p className="flex items-center gap-2 text-sm text-muted-foreground">
          <Spinner />
          Loading…
        </p>
      )}
      <ErrorAlert error={devices.error ?? push.test.error} />
      {devices.data?.data.length === 0 && (
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant="icon">
              <BellOff />
            </EmptyMedia>
            <EmptyTitle>No device is told yet</EmptyTitle>
            <EmptyDescription>
              Turn notifications on above on each device you want told: this computer, your phone.
            </EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {devices.data && devices.data.data.length > 0 && (
        <ItemGroup>
          {devices.data.data.map((device) => (
            <Item
              key={device.id}
              render={<li />}
              variant="outline"
              size="sm"
              aria-label={name(device)}
            >
              <ItemContent>
                <ItemTitle>
                  {name(device)}
                  {device.endpoint === push.endpoint && (
                    <Badge variant="secondary">This device</Badge>
                  )}
                </ItemTitle>
                <ItemDescription>
                  Added {dayFormat.format(new Date(device.created_at))}
                </ItemDescription>
              </ItemContent>
              <ItemActions>
                <TestButton push={push} device={device} />
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`Remove ${name(device)}`}
                  disabled={removing === device.id}
                  onClick={() => {
                    setRemoving(device.id)
                    push.remove.mutate(device, {
                      onSuccess: () => said(`${name(device)} removed`),
                      onSettled: () => setRemoving(null),
                    })
                  }}
                >
                  {removing === device.id ? <Spinner /> : <Trash2 />}
                </Button>
              </ItemActions>
            </Item>
          ))}
        </ItemGroup>
      )}
      <p className="text-xs text-muted-foreground">
        A device that stops taking notifications (the app removed, the site&apos;s data cleared)
        leaves this list by itself.
      </p>
    </section>
  )
}
