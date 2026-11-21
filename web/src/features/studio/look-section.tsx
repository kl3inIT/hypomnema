import { Captions, Frame, ImageIcon, Palette } from "lucide-react"
import { useEffect, useId, useRef, type ReactNode } from "react"

import type { VideoChrome, VideoLooks, VideoPreview } from "@/api/generated/types.gen"
import { Field, FieldDescription, FieldLabel } from "@/components/ui/field"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Spinner } from "@/components/ui/spinner"
import { Switch } from "@/components/ui/switch"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { errorText } from "@/lib/server-error"
import { cn } from "@/lib/utils"

import { paintBackground, templateColors } from "./backgrounds"
import {
  backgroundName,
  chromeHints,
  chromeNames,
  positionNames,
  positions,
  type LookFields,
} from "./look-state"
import { useLookSample } from "./use-look"

// LookSection is the Look of Make video and Change look (issue #908, row 16; #938): the look sample, then the
// caption style and where it sits, the frame around the phone's picture, and the background, each a group with its
// header as Cap's editor sidebar groups its controls (apps/desktop-gpui/src/editor_tabs.rs, editor_sidebar.rs;
// AGPLv3, layout only). The fields start from the default or the source revision; the sample and the request carry
// only the sections changed (look-state.ts). Padding and radius are only in the API and the CLI.
export function LookSection({
  looks,
  value,
  onChange,
  captions,
  sample,
  open,
}: {
  looks: VideoLooks
  value: LookFields
  onChange: (next: LookFields) => void
  // captions: the revision has captions; off, their style is not chosen.
  captions: boolean
  // sample: the look sample's request, its look the sections changed.
  sample: VideoPreview
  // open: the dialog is shown, so its sample is asked.
  open: boolean
}) {
  const id = useId()
  const full = value.frame.chrome === "full"
  const upright = (sample.aspect ?? "9:16") === "9:16"
  const presetNames = Object.fromEntries(looks.presets.map((p) => [p.id, p.name]))

  return (
    // An upright sample stands beside the fields where there is room, as Cap's preview beside its sidebar; a wide
    // one is above them.
    <section
      aria-label="Look"
      className={cn(
        "flex flex-col gap-4 rounded-xl border bg-card p-3",
        upright && "sm:flex-row sm:items-start",
      )}
    >
      <LookSample body={sample} open={open} />
      <div className="flex min-w-0 flex-1 flex-col gap-4">
        <Group icon={<Captions />} title="Captions" off={!captions && "Captions are off."}>
          <div className="grid gap-3 sm:grid-cols-2">
            <Field>
              <FieldLabel htmlFor={`${id}-preset`}>Style</FieldLabel>
              <Select
                value={value.captions.preset}
                onValueChange={(next) =>
                  next && onChange({ ...value, captions: { ...value.captions, preset: next } })
                }
                items={presetNames}
                disabled={!captions}
              >
                <SelectTrigger id={`${id}-preset`} width="full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {looks.presets.map((p) => (
                    <SelectItem key={p.id} value={p.id}>
                      {p.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
            <Field>
              <FieldLabel htmlFor={`${id}-position`}>Where</FieldLabel>
              <Select
                value={value.captions.position}
                onValueChange={(next) =>
                  next &&
                  onChange({
                    ...value,
                    captions: {
                      ...value.captions,
                      position: next as LookFields["captions"]["position"],
                    },
                  })
                }
                items={positionNames}
                disabled={!captions}
              >
                <SelectTrigger id={`${id}-position`} width="full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {positions.map((p) => (
                    <SelectItem key={p} value={p}>
                      {positionNames[p]}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          </div>
        </Group>
        <Group icon={<Frame />} title="Frame">
          <ToggleGroup
            aria-label="Frame"
            value={[value.frame.chrome]}
            onValueChange={(next: string[]) =>
              next[0] &&
              onChange({ ...value, frame: { ...value.frame, chrome: next[0] as VideoChrome } })
            }
            variant="choice"
            spacing={1}
          >
            {looks.chromes.map((c) => (
              <ToggleGroupItem key={c} value={c}>
                {chromeNames[c]}
              </ToggleGroupItem>
            ))}
          </ToggleGroup>
          <FieldDescription>{chromeHints[value.frame.chrome]}</FieldDescription>
          {!full && (
            <div className="grid grid-cols-2 gap-x-6 gap-y-2 sm:w-fit">
              <Field orientation="horizontal">
                <Switch
                  id={`${id}-shadow`}
                  checked={value.frame.shadow}
                  onCheckedChange={(on) =>
                    onChange({ ...value, frame: { ...value.frame, shadow: on } })
                  }
                />
                <FieldLabel htmlFor={`${id}-shadow`}>Shadow</FieldLabel>
              </Field>
              <Field orientation="horizontal">
                <Switch
                  id={`${id}-safe-top`}
                  checked={value.frame.safe_top}
                  onCheckedChange={(on) =>
                    onChange({ ...value, frame: { ...value.frame, safe_top: on } })
                  }
                />
                <FieldLabel htmlFor={`${id}-safe-top`}>Status strip</FieldLabel>
              </Field>
            </div>
          )}
        </Group>
        <Group
          icon={<ImageIcon />}
          title="Background"
          off={full && "A full picture has no background."}
        >
          {/* A swatch for each background, as Cap's wallpaper grid (editor_sidebar.rs, layout only), then two colours. */}
          <div className="grid grid-cols-4 gap-1.5">
            {looks.backgrounds.map((b) => (
              <SwatchButton
                key={b}
                name={backgroundName(b)}
                pressed={!value.background.colors && value.background.preset === b}
                onPress={() => onChange({ ...value, background: { preset: b } })}
              >
                <Swatch style={b} />
              </SwatchButton>
            ))}
            <SwatchButton
              name="Your colours"
              label="Your two colours"
              pressed={Boolean(value.background.colors)}
              onPress={() =>
                value.background.colors ||
                onChange({
                  ...value,
                  background: { colors: templateColors(value.background.preset ?? "midnight") },
                })
              }
            >
              <span className="flex aspect-[8/5] w-full items-center justify-center rounded-md border border-dashed">
                <Palette className="size-4 text-muted-foreground" />
              </span>
            </SwatchButton>
          </div>
          {value.background.colors && !full && (
            <div className="flex flex-wrap gap-4">
              {value.background.colors.map((c, i) => (
                <label key={i} className="flex items-center gap-2 text-sm">
                  <input
                    type="color"
                    value={c}
                    className="h-8 w-12 cursor-pointer rounded-md border bg-transparent p-0.5"
                    onChange={(event) => {
                      const colors = [...value.background.colors!] as [string, string]
                      colors[i] = event.target.value.toLowerCase()
                      onChange({ ...value, background: { colors } })
                    }}
                  />
                  {i === 0 ? "Top left" : "Bottom right"}
                </label>
              ))}
            </div>
          )}
        </Group>
      </div>
    </section>
  )
}

// Group is one control group: its icon and title, then its fields, dimmed with a reason when they do not apply.
function Group({
  icon,
  title,
  off,
  children,
}: {
  icon: ReactNode
  title: string
  off?: string | false
  children: ReactNode
}) {
  return (
    <fieldset className="flex min-w-0 flex-col gap-2" disabled={Boolean(off)}>
      <legend className="flex items-center gap-1.5 pb-2 text-sm font-medium [&_svg]:size-4 [&_svg]:text-muted-foreground">
        {icon}
        {title}
        {off && <span className="font-normal text-muted-foreground">· {off}</span>}
      </legend>
      <div className={cn("flex flex-col gap-2", off && "opacity-60")}>{children}</div>
    </fieldset>
  )
}

// SwatchButton is one background to pick: its picture and its name, ringed when it is the one chosen.
function SwatchButton({
  name,
  label,
  pressed,
  onPress,
  children,
}: {
  name: string
  label?: string
  pressed: boolean
  onPress: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      aria-label={label ?? name}
      title={label ?? name}
      onClick={onPress}
      className={cn(
        "flex min-w-0 flex-col items-center gap-1 rounded-lg p-1 text-[0.7rem] outline-none hover:bg-muted focus-visible:ring-3 focus-visible:ring-ring/50 disabled:pointer-events-none",
        pressed && "bg-primary/10 text-primary ring-2 ring-primary",
      )}
    >
      {children}
      <span className="w-full truncate">{name}</span>
    </button>
  )
}

// Swatch paints one of autodemo's backgrounds as the video would (backgrounds.ts).
function Swatch({ style }: { style: string }) {
  const ref = useRef<HTMLCanvasElement>(null)
  useEffect(() => {
    if (ref.current) paintBackground(ref.current, style)
  }, [style])

  return (
    <canvas
      ref={ref}
      width={96}
      height={60}
      aria-hidden
      className="aspect-[8/5] w-full rounded-md"
      data-testid="background-swatch"
    />
  )
}

// LookSample is the look over the take's picture at its first step, as the server draws it (row 14): it shows the
// look, not a frame the video promises to show.
function LookSample({ body, open }: { body: VideoPreview; open: boolean }) {
  const sample = useLookSample(body, open)
  const upright = (body.aspect ?? "9:16") === "9:16"

  return (
    <figure className={cn("flex shrink-0 flex-col items-center gap-1.5", upright && "sm:w-40")}>
      <div
        className={cn(
          "relative flex items-center justify-center overflow-hidden rounded-lg bg-muted",
          upright ? "aspect-[9/16] h-56 sm:h-auto sm:w-full" : "aspect-video w-full max-w-sm",
        )}
        data-testid="look-sample"
        aria-busy={sample.loading || undefined}
      >
        {sample.url && !sample.error && (
          <img
            src={sample.url}
            alt="A sample of the look"
            className={cn("size-full object-contain", sample.loading && "opacity-60")}
          />
        )}
        {sample.error ? (
          <p role="alert" className="px-3 text-center text-xs text-destructive">
            {errorText(sample.error)}
          </p>
        ) : (
          (sample.loading || !sample.url) && (
            <span className="absolute">
              <Spinner aria-label="Drawing the sample" />
            </span>
          )
        )}
      </div>
      <figcaption className="text-center text-xs text-muted-foreground">
        The look at the take's first step. The video may not show that moment.
      </figcaption>
    </figure>
  )
}
