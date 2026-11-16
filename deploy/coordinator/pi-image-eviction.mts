// A pi extension that keeps a chat full of images under the model gateway's request limit (issue #675). Installed
// root-owned by setup-coordinator.sh and given to every chat's pi by hypomnema-pi with --extension.
//
// pi counts an image as about 1,200 tokens whatever its size, and nothing in pi checks a request's bytes
// (compaction/compaction.ts, ESTIMATED_IMAGE_CHARS), so a Bot that reads many phone screenshots sends more bytes
// than the gateway takes long before pi compacts. Before each call to the model, on pi's `context` event (which gets
// a copy of the messages and changes only the request: extensions/types.ts, ContextEvent; the session file is never
// written), the oldest images of tool results are replaced with a line of text once the images are over a byte
// budget. This is Hermes's send-path eviction (agent/context_compressor.py, evict_stale_outbound_tool_images, with
// agent/image_eviction_policy.py), measured in bytes only:
//
// - The budget is Hermes's OUTBOUND_IMAGE_BUDGET_BYTES, 24,000,000 bytes of base64, under the 32 MiB the gateway
//   takes (modelgateway/chat.go, maxRequestBytes), which leaves room for the text.
// - Images go in batches of 8 (IMAGE_EVICTION_BATCH), and the count only grows by whole batches as history grows
//   (outbound_image_retire_count): Anthropic matches its prompt cache on the request's exact prefix, so evicting one
//   more image each call would miss the cache every call.
// - What was evicted stays evicted for the session (latched_retire_count), so the prefix does not change back.
// - Images a person sent are never replaced, and count against the budget (Hermes's "reserved" uploads).
// - The newest 3 images of tool results always stay (OUTBOUND_IMAGE_FLOOR), so the Bot still sees the screen it
//   acts on. Hermes lets the byte budget evict below them; issue #675's approved design keeps them, and pi's own
//   limit of 4.5 MB an image (utils/image-resize-core.ts) keeps three of them within the budget.
// - The text is Hermes's "[screenshot removed to save context]" with "image", as an image read from a file is
//   not a screenshot.
//
// Hermes's other trigger, a count of images per request (20 or 100), is not taken: the gateway's limit is in bytes.
// pi already treats the gateway's 413 as a context overflow and compacts once (ai/src/utils/overflow.ts), which is
// what remains when even this is not enough.

export const BUDGET_BYTES = 24_000_000;
export const BATCH = 8;
export const FLOOR = 3;
export const PLACEHOLDER = "[image removed to save context]";

// What this extension reads of pi's messages (pi-ai types.ts: ImageContent, ToolResultMessage, UserMessage).
type Part = { type: string; data?: string; [key: string]: unknown };
type Message = { role: string; toolCallId?: string; content?: string | Part[]; [key: string]: unknown };

// retireCount is how many of the oldest image-carrying tool results to replace, given the bytes of each, newest
// first, and the bytes of images that are never replaced. It is Hermes's outbound_image_retire_count with only its
// byte dimension: the largest count any shorter history (the oldest m carriers) would have needed, so it is a step
// function of history and never shrinks as messages are added.
export function retireCount(
	sizesNewestFirst: number[],
	reservedBytes: number,
	budget = BUDGET_BYTES,
	batch = BATCH,
	floor = FLOOR,
): number {
	const total = sizesNewestFirst.length;
	let retire = 0;
	for (let m = 1; m <= total; m++) {
		retire = Math.max(retire, retireForRequest(sizesNewestFirst.slice(total - m), reservedBytes, budget, batch, floor));
	}
	return retire;
}

// retireForRequest is Hermes's _retire_count_for_request for one request, in bytes: it retires in quanta of `batch`,
// narrowed to how many carriers fit above the floor, until what is kept fits the budget.
function retireForRequest(sizes: number[], reservedBytes: number, budget: number, batch: number, floor: number): number {
	const total = sizes.length;
	const kept = [0];
	for (const size of sizes) kept.push(kept[kept.length - 1] + size);
	const fits = (k: number) => reservedBytes + kept[k] <= budget;

	if (fits(total)) return 0;

	const shelter = Math.min(Math.max(floor, 0), total);
	let window = 0;
	if (fits(0)) {
		for (let k = total; k >= 0; k--) {
			if (fits(k)) {
				window = k;
				break;
			}
		}
	}
	const quantum = Math.max(1, Math.min(batch, window - shelter));

	let retire = 0;
	while (retire < total) {
		retire = Math.min(retire + quantum, total);
		if (fits(total - retire)) break;
	}
	return retire;
}

function images(m: Message): Part[] {
	return Array.isArray(m.content) ? m.content.filter((p) => p?.type === "image") : [];
}

// evict returns the messages to send, with the images of the oldest tool results replaced, or undefined when
// nothing changes. It never changes `messages`. `retired` holds the tool call ids evicted earlier in the session;
// the ones evicted now are added to it.
export function evict(
	messages: Message[],
	retired: Set<string>,
	budget = BUDGET_BYTES,
	batch = BATCH,
	floor = FLOOR,
): Message[] | undefined {
	const carriers: number[] = []; // indexes of tool results with images, newest first
	const sizes: number[] = [];
	let reserved = 0;

	for (let i = messages.length - 1; i >= 0; i--) {
		const bytes = images(messages[i]).reduce((sum, p) => sum + (typeof p.data === "string" ? p.data.length : 0), 0);
		if (bytes === 0 && images(messages[i]).length === 0) continue;
		if (messages[i].role === "toolResult") {
			carriers.push(i);
			sizes.push(bytes);
		} else {
			reserved += bytes;
		}
	}

	const total = carriers.length;
	let retire = retireCount(sizes, reserved, budget, batch, floor);

	// What was evicted before stays evicted: every carrier up to the newest one evicted earlier goes.
	const latched = carriers.findIndex((i) => retired.has(messages[i].toolCallId ?? ""));
	if (latched >= 0) retire = Math.max(retire, total - latched);
	retire = Math.min(retire, Math.max(total - floor, 0));
	if (retire === 0) return undefined;

	const out = messages.slice();
	for (const i of carriers.slice(total - retire)) {
		const m = messages[i];
		if (m.toolCallId) retired.add(m.toolCallId);
		out[i] = {
			...m,
			content: (m.content as Part[]).map((p) => (p?.type === "image" ? { type: "text", text: PLACEHOLDER } : p)),
		};
	}
	return out;
}

// What this extension uses of pi's ExtensionAPI and ExtensionContext (extensions/types.ts).
type Pi = {
	on(
		event: "context",
		handler: (
			event: { messages: Message[] },
			ctx: { sessionManager: { getSessionId(): string } },
		) => { messages: Message[] } | undefined,
	): unknown;
};

export default function imageEviction(pi: Pi): void {
	// One session's evictions (Hermes keys them on the session id: a new session starts clean).
	let session = "";
	let retired = new Set<string>();

	pi.on("context", (event, ctx) => {
		const id = ctx.sessionManager.getSessionId();
		if (id !== session) {
			session = id;
			retired = new Set();
		}
		const messages = evict(event.messages, retired);
		return messages ? { messages } : undefined;
	});
}
