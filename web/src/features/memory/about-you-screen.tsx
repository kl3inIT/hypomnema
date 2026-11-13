import { SettingsPage } from "@/features/settings/settings-shell"

import { MemoryEditor } from "./memory-editor"
import { useOwnerMemory, useSetOwnerMemory } from "./use-memory"

// AboutYouScreen is the memory about the owner that every worker and Bot gets (issue #21), as OpenMausBot's "About
// me" under its settings, which every bot reads: how he works, his language, his standing rules. Agents add to it
// as they learn; he edits it here.
export function AboutYouScreen() {
  const memory = useOwnerMemory()
  const save = useSetOwnerMemory()

  return (
    <SettingsPage
      title="About you"
      description="What every agent and Bot knows about you. They add to it as they learn; you can edit it."
    >
      {memory.error ? (
        <p className="py-6 text-sm text-destructive">This memory could not be read.</p>
      ) : !memory.data ? (
        <p className="py-6 text-sm text-muted-foreground">Loading…</p>
      ) : (
        <MemoryEditor
          id="owner-memory"
          label="Memory"
          memory={memory.data.memory}
          limit={memory.data.memory_limit}
          writer="An agent"
          help="One entry a line: how you work, your language, rules for every agent. Workers get it in their first message, Bots from their next chat on. Keep no secret in it."
          save={{
            mutate: (text, { onSuccess }) =>
              save.mutate(text, { onSuccess: (m) => onSuccess(m.memory) }),
            isPending: save.isPending,
            error: save.error,
            reset: save.reset,
          }}
        />
      )}
    </SettingsPage>
  )
}
