import { useForm } from "@tanstack/react-form"
import { z } from "zod"

import type { Routine, RoutineSettings } from "@/api/generated/types.gen"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogPanel,
  DialogTitle,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "@/components/ui/toast"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { useBots } from "@/features/bots/use-bots"
import { ErrorAlert, TextField } from "@/features/settings/settings-parts"

import { ScheduleFields } from "./schedule-fields"
import { draftOf, newDraft, scheduleOf, type Draft } from "./schedule-draft"
import { usePutRoutine } from "./use-routines"

// RoutineForm makes a Routine or changes one (issue #211), in a dialog with its fields in the order OpenMausBot's
// routine editor asks them (Apache License 2.0, Copyright 2026 Milind Soni and OpenMausBot contributors,
// https://github.com/milind-soni/OpenMausBot; src/components/RoutineCalendarPage.tsx L864-1011: the name, when,
// who, what to do; Cancel and Save at the foot). Changed: a Routine is a prompt or a script file whose text the
// owner reads here; the Bot and the name do not change once it is made, since a Routine is known by both.

const nameRule = /^[a-z][a-z0-9-]{0,62}$/
const fileRule = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/

const schema = z
  .object({
    bot: z.string().min(1, "Choose a Bot."),
    name: z
      .string()
      .regex(nameRule, "Lowercase letters, digits and hyphens, starting with a letter."),
    kind: z.enum(["prompt", "script"]),
    prompt: z.string(),
    script: z.string(),
    scriptText: z.string(),
    summary: z.string(),
    timeout: z.number(),
    announce: z.boolean(),
    draft: z.custom<Draft>(),
  })
  .superRefine((v, ctx) => {
    if (v.kind === "prompt" && !v.prompt.trim()) {
      ctx.addIssue({ code: "custom", path: ["prompt"], message: "Say what the Bot does." })
    }
    if (v.kind === "script" && !fileRule.test(v.script)) {
      ctx.addIssue({
        code: "custom",
        path: ["script"],
        message: "A file's name, such as sync.sh: no folder, no command line.",
      })
    }
    if (v.kind === "script" && !v.scriptText.trim()) {
      ctx.addIssue({ code: "custom", path: ["scriptText"], message: "Write the script." })
    }
    if (v.kind === "script" && (!Number.isInteger(v.timeout) || v.timeout < 1)) {
      ctx.addIssue({ code: "custom", path: ["timeout"], message: "At least 1 second." })
    }
    if (
      v.draft.choice === "minutes" &&
      (!Number.isInteger(v.draft.minutes) || v.draft.minutes < 5 || v.draft.minutes > 1440)
    ) {
      ctx.addIssue({ code: "custom", path: ["draft"], message: "Every 5 to 1440 minutes." })
    }
  })

type Values = z.infer<typeof schema>

function valuesOf(routine: Routine | undefined, bot: string): Values {
  return {
    bot: routine?.bot ?? bot,
    name: routine?.name ?? "",
    kind: routine?.kind ?? "prompt",
    prompt: routine?.prompt ?? "",
    script: routine?.script ?? "",
    scriptText: routine?.script_text ?? "",
    summary: routine?.summary ?? "",
    timeout: routine?.kind === "script" ? routine.timeout_seconds : 600,
    announce: routine?.announce ?? true,
    draft: routine ? draftOf(routine) : newDraft(),
  }
}

function settingsOf(v: Values, routine: Routine | undefined): RoutineSettings {
  return {
    kind: v.kind,
    ...(v.kind === "prompt"
      ? { prompt: v.prompt.trim() }
      : { script: v.script.trim(), script_text: v.scriptText, timeout_seconds: v.timeout }),
    ...scheduleOf(v.draft),
    summary: v.summary.trim() || undefined,
    chat_id: routine?.chat_id,
    announce: v.announce,
  }
}

export function RoutineForm({
  open,
  onOpenChange,
  routine,
  bot = "",
  onSaved,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  // routine is the one being changed; absent for a new one.
  routine?: Routine
  // bot is the Bot a new Routine is for, when the page is one Bot's.
  bot?: string
  onSaved?: (routine: Routine) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{routine ? `Edit ${routine.name}` : "New Routine"}</DialogTitle>
          <DialogDescription>
            Work a Bot does on a schedule: a prompt it works on with its model, or a script it runs
            with no model.
          </DialogDescription>
        </DialogHeader>
        {open && (
          <Body
            routine={routine}
            bot={bot}
            onSaved={(r) => {
              onOpenChange(false)
              onSaved?.(r)
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function Body({
  routine,
  bot,
  onSaved,
}: {
  routine?: Routine
  bot: string
  onSaved: (routine: Routine) => void
}) {
  const bots = useBots()
  const put = usePutRoutine()
  const form = useForm({
    defaultValues: valuesOf(routine, bot || (bots.data?.[0]?.name ?? "")),
    validators: { onSubmit: schema },
    onSubmit: async ({ value }) => {
      const saved = await put.mutateAsync({
        bot: value.bot,
        name: value.name,
        settings: settingsOf(value, routine),
      })
      toast.add({ title: routine ? "Routine saved" : "Routine made", type: "success" })
      onSaved(saved)
    },
  })

  return (
    <>
      <DialogPanel>
        <form
          id="routine-form"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            {!routine && (
              <div className="grid gap-4 sm:grid-cols-2">
                <form.Field name="bot">
                  {(field) => (
                    <Field>
                      <FieldLabel htmlFor="routine-bot">Bot</FieldLabel>
                      <Select
                        value={field.state.value}
                        onValueChange={(next) => next && field.handleChange(next)}
                        disabled={Boolean(bot)}
                      >
                        <SelectTrigger id="routine-bot" width="full">
                          <SelectValue placeholder="Choose a Bot" />
                        </SelectTrigger>
                        <SelectContent>
                          {(bots.data ?? []).map((b) => (
                            <SelectItem key={b.name} value={b.name}>
                              {b.name}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <FieldError errors={field.state.meta.errors} />
                    </Field>
                  )}
                </form.Field>
                <form.Field name="name">
                  {(field) => <TextField field={field} label="Name" placeholder="morning-brief" />}
                </form.Field>
              </div>
            )}

            <form.Field name="draft">
              {(field) => (
                <Field>
                  <FieldLabel>When</FieldLabel>
                  <ScheduleFields value={field.state.value} onChange={field.handleChange} />
                  <FieldError errors={field.state.meta.errors} />
                </Field>
              )}
            </form.Field>

            <form.Field name="kind">
              {(field) => (
                <Field>
                  <FieldLabel>What it does</FieldLabel>
                  <ToggleGroup
                    aria-label="What it does"
                    value={[field.state.value]}
                    onValueChange={(next: string[]) =>
                      next[0] && field.handleChange(next[0] as Values["kind"])
                    }
                    variant="choice"
                    size="sm"
                    spacing={1}
                  >
                    <ToggleGroupItem value="prompt">A prompt</ToggleGroupItem>
                    <ToggleGroupItem value="script">A script</ToggleGroupItem>
                  </ToggleGroup>
                </Field>
              )}
            </form.Field>

            <form.Subscribe selector={(s) => s.values.kind}>
              {(kind) =>
                kind === "prompt" ? (
                  <form.Field name="prompt">
                    {(field) => (
                      <Field data-invalid={!field.state.meta.isValid || undefined}>
                        <FieldLabel htmlFor="routine-prompt">Prompt</FieldLabel>
                        <Textarea
                          id="routine-prompt"
                          rows={5}
                          value={field.state.value}
                          onChange={(e) => field.handleChange(e.target.value)}
                          placeholder="Summarize what changed overnight in the repositories."
                        />
                        <FieldDescription>
                          The Bot works on it with its model in a new chat each run.
                        </FieldDescription>
                        <FieldError errors={field.state.meta.errors} />
                      </Field>
                    )}
                  </form.Field>
                ) : (
                  <>
                    <div className="grid gap-4 sm:grid-cols-[1fr_9rem]">
                      <form.Field name="script">
                        {(field) => (
                          <TextField
                            field={field}
                            label="Script file"
                            placeholder="sync.sh"
                            description="Kept in the Bot's routines folder."
                          />
                        )}
                      </form.Field>
                      <form.Field name="timeout">
                        {(field) => (
                          <Field data-invalid={!field.state.meta.isValid || undefined}>
                            <FieldLabel htmlFor="routine-timeout">Time limit (s)</FieldLabel>
                            <Input
                              id="routine-timeout"
                              type="number"
                              min={1}
                              value={field.state.value || ""}
                              onChange={(e) => field.handleChange(Number(e.target.value))}
                            />
                            <FieldError errors={field.state.meta.errors} />
                          </Field>
                        )}
                      </form.Field>
                    </div>
                    <form.Field name="scriptText">
                      {(field) => (
                        <Field data-invalid={!field.state.meta.isValid || undefined}>
                          <FieldLabel htmlFor="routine-script-text">Script</FieldLabel>
                          <Textarea
                            id="routine-script-text"
                            rows={8}
                            spellCheck={false}
                            value={field.state.value}
                            onChange={(e) => field.handleChange(e.target.value)}
                            placeholder={"#!/bin/sh\nhypomnema superset chart 9 164 --format csv"}
                            code
                          />
                          <FieldDescription>
                            It runs with the Bot's tools and no model. What runs is what is written
                            here.
                          </FieldDescription>
                          <FieldError errors={field.state.meta.errors} />
                        </Field>
                      )}
                    </form.Field>
                  </>
                )
              }
            </form.Subscribe>

            <form.Field name="summary">
              {(field) => (
                <TextField
                  field={field}
                  label="Summary"
                  placeholder="the usage figures go to the Lark Base"
                  description="Ends the sentence that says the Routine."
                />
              )}
            </form.Field>

            {routine?.chat_id && (
              <form.Field name="announce">
                {(field) => (
                  <Field orientation="horizontal">
                    <Switch
                      id="routine-announce"
                      checked={field.state.value}
                      onCheckedChange={(next) => field.handleChange(next)}
                    />
                    <FieldLabel htmlFor="routine-announce">
                      Tell each result in the chat it was made in
                    </FieldLabel>
                  </Field>
                )}
              </form.Field>
            )}

            <ErrorAlert error={put.error} />
          </FieldGroup>
        </form>
      </DialogPanel>
      <DialogFooter>
        <DialogClose render={<Button variant="outline" />}>Cancel</DialogClose>
        <form.Subscribe selector={(s) => s.isSubmitting}>
          {(submitting) => (
            <Button type="submit" form="routine-form" disabled={submitting}>
              {submitting ? "Saving…" : routine ? "Save" : "Make it"}
            </Button>
          )}
        </form.Subscribe>
      </DialogFooter>
    </>
  )
}
