package nodeexec

import (
	"strconv"
	"strings"
	"sync"
)

// headTail keeps the first and the last half of what is written to it and
// counts what it leaves out, as Coder's process output keeps the start and
// the end of a long run (agent/agentproc/headtail.go): the start says what
// the command did, the end how it ended.
type headTail struct {
	mu    sync.Mutex
	half  int
	head  []byte
	tail  []byte // a ring of half bytes once full
	next  int    // where the ring is written next
	total int64
}

func newHeadTail(size int) *headTail {
	return &headTail{half: size / 2}
}

// Write keeps p's bytes in the head until it is full, then in the tail.
func (b *headTail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.total += int64(len(p))
	rest := p

	if room := b.half - len(b.head); room > 0 {
		n := min(room, len(rest))
		b.head = append(b.head, rest[:n]...)
		rest = rest[n:]
	}

	// Only the last half of what is left can stay in the tail.
	if len(rest) > b.half {
		rest = rest[len(rest)-b.half:]
	}

	for len(rest) > 0 {
		if len(b.tail) < b.half {
			n := min(b.half-len(b.tail), len(rest))
			b.tail = append(b.tail, rest[:n]...)
			rest = rest[n:]

			continue
		}

		n := copy(b.tail[b.next:], rest)
		b.next = (b.next + n) % b.half
		rest = rest[n:]
	}

	return len(p), nil
}

// String is what was kept, with a line saying where the middle was left
// out, and how many bytes were.
func (b *headTail) String() (string, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	tail := b.tail
	if len(b.tail) == b.half && b.next > 0 {
		tail = append(append([]byte{}, b.tail[b.next:]...), b.tail[:b.next]...)
	}

	cut := b.total - int64(len(b.head)) - int64(len(tail))
	if cut <= 0 {
		return strings.ToValidUTF8(string(b.head)+string(tail), "�"), 0
	}

	return strings.ToValidUTF8(string(b.head), "�") + "\n[… " + strconv.FormatInt(cut, 10) + " bytes left out …]\n" +
		strings.ToValidUTF8(string(tail), "�"), cut
}
