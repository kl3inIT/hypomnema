// newChatId is an id for a chat not made yet, of the shape the server makes them (platform.NewID("cht")): "cht_"
// and 16 random bytes in base32 with a-z and 2-7, 26 characters. The New chat page chooses it when it opens, so
// that the files attached before the first message have the chat's folder (issue #357), as t3code's newThreadId()
// names a thread before it exists (apps/web/src/components/chat/useComposerSend.ts).
const alphabet = "abcdefghijklmnopqrstuvwxyz234567"

export function newChatId() {
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  let id = "cht_"
  let bits = 0
  let value = 0
  for (const byte of bytes) {
    value = (value << 8) | byte
    bits += 8
    while (bits >= 5) {
      id += alphabet[(value >>> (bits - 5)) & 31]
      bits -= 5
    }
    value &= (1 << bits) - 1
  }
  if (bits > 0) id += alphabet[(value << (5 - bits)) & 31]

  return id
}
