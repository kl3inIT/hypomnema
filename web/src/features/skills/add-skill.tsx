import { useMutation, useQueryClient } from "@tanstack/react-query"
import { useNavigate } from "@tanstack/react-router"
import { Search, Upload } from "lucide-react"
import { useId, useRef, useState, type ReactElement } from "react"

import "@/api/client"
import { addSkill, uploadSkill } from "@/api/generated/sdk.gen"
import type { Skill, SkillSearchResult } from "@/api/generated/types.gen"
import { GitHubIcon } from "@/components/icons/brand-icons"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { said } from "@/features/settings/use-integrations"
import { ErrorAlert } from "@/features/settings/settings-parts"
import { cn } from "@/lib/utils"

import { SkillsShSearch } from "./skills-sh"
import { size } from "./skills-state"
import { skillsKey } from "./use-skills"

// The largest archive the server takes (internal/shared/skillpack MaxArchive).
const maxArchive = 25 * 1024 * 1024

type Way = "github" | "upload" | "skills-sh"

// AddSkill is the button and dialog that add a skill, or its next revision: from a public GitHub repository, as
// OpenMausBot imports one (src/components/bot-settings/SkillsSection.tsx, one field for owner/repo), or from an
// archive, as Omnara uploads one (components/skills/SkillSourcePicker.tsx, a place to drop it or a button to choose
// it), or found on skills.sh (skills-sh.tsx). Under tabs, as Omnara's CreateSkillDialog has its Upload and SKILL.md.
// The server reads the skill's
// name from its SKILL.md; files equal to its newest revision add nothing. On success the dialog closes on the
// skill's page, where it is given to everyone.
export function AddSkill({
  trigger,
  way: first = "github",
  github: given = "",
}: {
  trigger: ReactElement
  way?: Way
  // github fills the field, as the skill's page does to fetch its source again.
  github?: string
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [open, setOpen] = useState(false)
  const [way, setWay] = useState<Way>(first)
  const [github, setGithub] = useState(given)
  const [file, setFile] = useState<File | null>(null)
  const [picked, setPicked] = useState<SkillSearchResult | null>(null)

  const add = useMutation({
    mutationFn: async (): Promise<{ skill: Skill; added: boolean }> => {
      if (way === "github") {
        const res = await addSkill({ body: { github: github.trim() } })

        return { skill: res.data, added: res.response.status === 201 }
      }

      if (way === "skills-sh") {
        if (!picked) throw new Error("Pick a skill first.")

        // skills.sh names the skill, not its folder: the control plane finds the folder as the skills CLI does.
        const res = await addSkill({ body: { github: picked.source, skill: picked.name } })

        return { skill: res.data, added: res.response.status === 201 }
      }

      if (!file) throw new Error("Choose an archive first.")
      if (file.size > maxArchive) throw new Error(`${file.name} is over 25 MB.`)

      const res = await uploadSkill({ body: file })

      return { skill: res.data, added: res.response.status === 201 }
    },
    onSuccess: async ({ skill, added }) => {
      await queryClient.invalidateQueries({ queryKey: skillsKey })
      setOpen(false)
      said(
        added
          ? `${skill.name}: version ${skill.latest.revision} added`
          : `${skill.name}: the files are those of version ${skill.latest.revision}; nothing added`,
      )
      await navigate({ to: "/settings/skills/$name", params: { name: skill.name } })
    },
  })

  function change(next: boolean) {
    setOpen(next)
    if (next) {
      setWay(first)
      setGithub(given)
      setFile(null)
      setPicked(null)
      add.reset()
    }
  }

  const ready =
    way === "github" ? github.trim() !== "" : way === "upload" ? file !== null : picked !== null

  return (
    <Dialog open={open} onOpenChange={change}>
      <DialogTrigger render={trigger} />
      <DialogContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            if (ready) add.mutate()
          }}
        >
          <DialogHeader>
            <DialogTitle>Add a skill</DialogTitle>
            <DialogDescription>
              A folder with a SKILL.md. One whose name is already here becomes its next version.
            </DialogDescription>
          </DialogHeader>
          <Tabs
            value={way}
            onValueChange={(next) => {
              setWay(next as Way)
              add.reset()
            }}
          >
            <TabsList>
              <TabsTrigger value="github">
                <GitHubIcon className="size-3.5" />
                GitHub
              </TabsTrigger>
              <TabsTrigger value="upload">
                <Upload className="size-3.5" />
                Upload
              </TabsTrigger>
              <TabsTrigger value="skills-sh">
                <Search className="size-3.5" />
                skills.sh
              </TabsTrigger>
            </TabsList>
            <TabsContent value="github">
              <Field>
                <FieldLabel htmlFor="skill-github">Repository and folder</FieldLabel>
                <Input
                  id="skill-github"
                  autoComplete="off"
                  spellCheck={false}
                  placeholder="anthropics/skills/skills/pdf@main"
                  value={github}
                  onChange={(event) => setGithub(event.target.value)}
                />
                <FieldDescription>
                  owner/repo, then the skill's folder, then @ a branch, tag or commit; without @,
                  the default branch. Public repositories only.
                </FieldDescription>
              </Field>
            </TabsContent>
            <TabsContent value="upload">
              <DropArchive file={file} onFile={setFile} />
            </TabsContent>
            <TabsContent value="skills-sh">
              <SkillsShSearch picked={picked} onPick={setPicked} />
            </TabsContent>
          </Tabs>
          <ErrorAlert error={add.error} />
          <DialogFooter>
            <DialogClose render={<Button variant="ghost" type="button" />}>Cancel</DialogClose>
            <Button type="submit" disabled={!ready || add.isPending}>
              {add.isPending && <Spinner data-icon="inline-start" />}
              {way === "upload" ? "Upload" : "Add"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// DropArchive is Omnara's SkillSourcePicker for one archive (Apache-2.0, Copyright Omnara;
// frontend/apps/web/src/components/skills/SkillSourcePicker.tsx): a place to drop it, or a button that opens the
// file chooser. Changed: one .zip or .tar.gz only, since the server unpacks it; no folder, which a browser hands
// over as loose files.
function DropArchive({ file, onFile }: { file: File | null; onFile: (file: File) => void }) {
  const input = useRef<HTMLInputElement>(null)
  const id = useId()
  const [over, setOver] = useState(false)

  return (
    <div
      onDragOver={(event) => {
        event.preventDefault()
        setOver(true)
      }}
      onDragLeave={() => setOver(false)}
      onDrop={(event) => {
        event.preventDefault()
        setOver(false)
        const dropped = event.dataTransfer.files[0]
        if (dropped) onFile(dropped)
      }}
      className={cn(
        "flex flex-col items-center gap-2 rounded-lg border border-dashed px-4 py-6 text-center text-sm text-muted-foreground",
        over && "border-ring bg-muted/40",
      )}
    >
      <Upload className="size-5" aria-hidden />
      {file ? (
        <p className="text-foreground">
          <span>{file.name}</span>{" "}
          <span className="text-muted-foreground">· {size(file.size)}</span>
        </p>
      ) : (
        <>
          <p>Drop a .zip or .tar.gz here</p>
          <p className="text-xs">A skill's folder with its SKILL.md, at most 25 MB</p>
        </>
      )}
      <label htmlFor={id} className="sr-only">
        Archive
      </label>
      <input
        ref={input}
        id={id}
        type="file"
        accept=".zip,.tar.gz,.tgz,application/zip,application/gzip"
        className="hidden"
        onChange={(event) => {
          const chosen = event.target.files?.[0]
          if (chosen) onFile(chosen)
        }}
      />
      <Button type="button" variant="outline" size="sm" onClick={() => input.current?.click()}>
        {file ? "Choose another" : "Choose a file"}
      </Button>
    </div>
  )
}
