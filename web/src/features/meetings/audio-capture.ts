import workletUrl from "./pcm-capture.worklet.ts?worker&url"

// Capturing a stream's sound as PCM16 24 kHz mono for a meeting recorded live (issue #820), adapted from MemoryOS's
// startStreamCapture (web/src/features/voice/capture/audio-capture.ts): an AudioWorklet in its own audio context;
// stopping it stops every track of the stream and closes the context, as a failed start does.

export type AudioCaptureHandlers = {
  // onChunk is PCM16 little-endian mono 24 kHz, about 100 ms a chunk.
  onChunk: (pcm: ArrayBuffer) => void
  // onLevel is the RMS level of the latest chunk, 0 to 1.
  onLevel: (level: number) => void
}

export type AudioCapture = { stop: () => void }

export async function startStreamCapture(
  stream: MediaStream,
  handlers: AudioCaptureHandlers,
): Promise<AudioCapture> {
  const context = new AudioContext()
  try {
    // A permission prompt can outlast the click's activation, which leaves a new context suspended.
    if (context.state === "suspended") await context.resume()
    await context.audioWorklet.addModule(workletUrl)
    const source = context.createMediaStreamSource(stream)
    const node = new AudioWorkletNode(context, "hypomnema-pcm-capture", {
      numberOfInputs: 1,
      numberOfOutputs: 0,
      channelCount: 1,
    })
    node.port.onmessage = (event: MessageEvent<{ pcm: ArrayBuffer; level: number }>) => {
      handlers.onChunk(event.data.pcm)
      handlers.onLevel(event.data.level)
    }
    source.connect(node)
    let stopped = false

    return {
      stop() {
        if (stopped) return
        stopped = true
        node.port.onmessage = null
        source.disconnect()
        node.disconnect()
        for (const track of stream.getTracks()) track.stop()
        void context.close()
      },
    }
  } catch (error) {
    for (const track of stream.getTracks()) track.stop()
    void context.close()
    throw error
  }
}
