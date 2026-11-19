// The AudioWorklet that turns a stream's sound into PCM16 little-endian mono at 24 kHz, the format a meeting's track is
// streamed in (issue #820). Adapted from MemoryOS's (web/src/features/voice/capture/pcm-capture.worklet.ts, the
// owner's own): 100 ms chunks with their RMS level, posted on the audio thread's schedule, nothing buffered beyond one
// chunk, so a slow page never makes the worklet drop audio.

declare const sampleRate: number
declare function registerProcessor(
  name: string,
  processor: new () => AudioWorkletProcessorLike,
): void
declare class AudioWorkletProcessor {
  readonly port: MessagePort
}
interface AudioWorkletProcessorLike {
  process(inputs: Float32Array[][]): boolean
}

const targetRate = 24_000
const chunkSamples = targetRate / 10

class PcmCaptureProcessor extends AudioWorkletProcessor implements AudioWorkletProcessorLike {
  private readonly step = sampleRate / targetRate
  // position is the read position in the current block; -1 ≤ position < 0 reads from the previous block.
  private position = 0
  private previous = 0
  private chunk = new Int16Array(chunkSamples)
  private filled = 0
  private squares = 0

  process(inputs: Float32Array[][]): boolean {
    const channel = inputs[0]?.[0]
    if (!channel || channel.length === 0) return true

    let position = this.position
    while (position < channel.length - 1) {
      const index = Math.floor(position)
      const from = index < 0 ? this.previous : channel[index]!
      const to = channel[index + 1]!
      const value = Math.max(-1, Math.min(1, from + (to - from) * (position - index)))
      const sample = value < 0 ? value * 0x8000 : value * 0x7fff
      this.chunk[this.filled++] = sample
      this.squares += sample * sample
      if (this.filled === chunkSamples) this.flush()
      position += this.step
    }

    this.previous = channel[channel.length - 1]!
    this.position = position - channel.length

    return true
  }

  private flush() {
    const level = Math.sqrt(this.squares / chunkSamples) / 0x8000
    const pcm = this.chunk.buffer
    this.port.postMessage({ pcm, level }, [pcm])
    this.chunk = new Int16Array(chunkSamples)
    this.filled = 0
    this.squares = 0
  }
}

registerProcessor("hypomnema-pcm-capture", PcmCaptureProcessor)
