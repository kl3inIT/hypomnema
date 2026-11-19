// What a meeting recorded live hears in this browser (issue #820), adapted from MemoryOS's meeting-capture.ts after
// Nojoin's featureDetect: Chromium on a desktop can share a meeting's tab with its sound, so a meeting held online
// records the microphone and the tab; a phone, or another browser, records the microphone only.

export type CaptureSupport = "sharedAudio" | "microphoneOnly" | "unsupported"

type NavigatorLike = Pick<Navigator, "userAgent" | "mediaDevices"> & {
  userAgentData?: { mobile?: boolean; brands?: { brand: string }[] }
}

// captureSupport is Nojoin's order: a phone records the microphone only; on a desktop, Chromium shares a tab's
// sound and another browser records the microphone only.
export function captureSupport(nav: NavigatorLike = navigator): CaptureSupport {
  const agent = nav.userAgent
  const media = nav.mediaDevices
  const hasMicrophone = typeof media?.getUserMedia === "function"
  const hasDisplay = typeof media?.getDisplayMedia === "function"
  if (!hasMicrophone) return "unsupported"

  const mobile = nav.userAgentData?.mobile ?? /Android|iPhone|iPad|iPod|Mobile/i.test(agent)
  const chromium =
    nav.userAgentData?.brands?.some((b) => /Chromium|Google Chrome|Microsoft Edge/.test(b.brand)) ??
    (/Chrome\/|Edg\//.test(agent) && !/OPR\//.test(agent))
  if (mobile || !chromium || !hasDisplay) return "microphoneOnly"

  return "sharedAudio"
}

// ShareCancelledError: the share picker was closed; nothing was started.
class ShareCancelledError extends Error {
  constructor(cause: unknown) {
    super("Share picker cancelled", { cause })
    this.name = "ShareCancelledError"
  }
}

// NoTabAudioError: a tab was shared without its sound.
class NoTabAudioError extends Error {
  constructor() {
    super("The shared tab has no sound")
    this.name = "NoTabAudioError"
  }
}

// pickMeetingTab asks to share the meeting's tab with its sound. Chrome offers the sound only when video is asked, so
// the video is stopped at once (OpenNoteTaker), and the tab's sound is taken as it is (silent-notetaker).
async function pickMeetingTab(media: MediaDevices = navigator.mediaDevices) {
  let shared: MediaStream
  try {
    shared = await media.getDisplayMedia({
      video: true,
      audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: false },
      systemAudio: "include",
      selfBrowserSurface: "exclude",
      surfaceSwitching: "include",
    } as DisplayMediaStreamOptions)
  } catch (error) {
    throw new ShareCancelledError(error)
  }

  for (const track of shared.getVideoTracks()) track.stop()
  const audio = shared.getAudioTracks()
  if (audio.length === 0) throw new NoTabAudioError()

  return new MediaStream(audio)
}

// openMicrophone is the microphone with the browser's echo cancellation, noise suppression and gain control.
function openMicrophone(media: MediaDevices = navigator.mediaDevices) {
  return media.getUserMedia({
    audio: {
      channelCount: 1,
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
    },
  })
}

// openMeetingSources opens what a meeting records in Nojoin's order: the share picker first, while the click still
// counts as the person's, then the microphone.
export async function openMeetingSources(
  kind: "online" | "in_person",
  media: MediaDevices = navigator.mediaDevices,
): Promise<{ microphone: MediaStream; tab: MediaStream | null }> {
  const tab = kind === "online" ? await pickMeetingTab(media) : null
  try {
    return { microphone: await openMicrophone(media), tab }
  } catch (error) {
    for (const track of tab?.getTracks() ?? []) track.stop()
    throw error
  }
}

// sourceError says why the sources could not be opened, in the person's words.
export function sourceError(error: unknown) {
  if (error instanceof ShareCancelledError) return "The tab was not shared, so nothing was started."
  if (error instanceof NoTabAudioError)
    return "The tab was shared without its sound. Share it again and turn on “Share tab audio”."
  if (
    error instanceof DOMException &&
    (error.name === "NotAllowedError" || error.name === "SecurityError")
  )
    return "The browser was not allowed to use the microphone. Allow it in the site's settings."

  return "The microphone could not be opened."
}
