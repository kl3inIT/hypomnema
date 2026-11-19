import { Link, useNavigate } from "@tanstack/react-router"
import { ChevronRight } from "lucide-react"

import { PageHeader } from "@/components/page-header"
import { useTake } from "@/features/takes/take-state"
import { TakeView } from "@/features/takes/take-view"

import { useVideo } from "./studio-state"
import { VideoView } from "./video-view"

// The take's and the video's own pages in the Studio (issue #818, row 12): /studio/takes/:id and /studio/videos/:id
// open them directly, as the meeting's page opens one meeting (../meetings/meeting-screen.tsx), with the take's tab
// and the video's tab of a pane filling the page. A take's video opens on its page, and a video's take on its; a
// delete goes back to the Studio.

function Crumbs({ here }: { here: string }) {
  return (
    <span className="flex min-w-0 items-center gap-1.5">
      <Link to="/studio" className="text-muted-foreground hover:text-foreground">
        Studio
      </Link>
      <ChevronRight className="size-3.5 shrink-0 text-muted-foreground/60" aria-hidden />
      <span className="truncate">{here}</span>
    </span>
  )
}

export function StudioTakeScreen({ id }: { id: string }) {
  const title = useTake(id).data?.title ?? "Take"
  const navigate = useNavigate()

  return (
    <main className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here={title} />} />
      <div className="flex min-h-0 flex-1 flex-col border-t">
        <TakeView
          id={id}
          onClose={() => void navigate({ to: "/studio" })}
          onOpenVideo={(video) =>
            void navigate({ to: "/studio/videos/$videoId", params: { videoId: video } })
          }
        />
      </div>
    </main>
  )
}

export function StudioVideoScreen({ id }: { id: string }) {
  const title = useVideo(id).data?.title ?? "Video"
  const navigate = useNavigate()

  return (
    <main className="flex min-h-0 min-w-0 flex-1 flex-col">
      <PageHeader title={<Crumbs here={title} />} />
      <div className="flex min-h-0 flex-1 flex-col border-t">
        <VideoView
          id={id}
          onClose={() => void navigate({ to: "/studio" })}
          onOpenTake={(take) =>
            void navigate({ to: "/studio/takes/$takeId", params: { takeId: take } })
          }
        />
      </div>
    </main>
  )
}
