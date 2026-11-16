// Tests of pi-image-eviction.mts (issue #675): `node --test deploy/coordinator/pi-image-eviction.test.mts`.
import assert from "node:assert/strict";
import { test } from "node:test";

import imageEviction, { BATCH, BUDGET_BYTES, evict, FLOOR, PLACEHOLDER } from "./pi-image-eviction.mts";

const MB = 1_000_000;

// A phone screenshot as a tool result, its image `size` bytes of base64.
function screen(n: number, size = MB) {
	return {
		role: "toolResult",
		toolCallId: `call_${n}`,
		toolName: "device_screen",
		content: [
			{ type: "text", text: `screen ${n}` },
			{ type: "image", data: "A".repeat(size), mimeType: "image/jpeg" },
		],
		isError: false,
		timestamp: n,
	};
}

// A chat of `n` screenshots, each after the assistant's call for it.
function chat(n: number, size = MB) {
	const messages: any[] = [{ role: "user", content: "check the queue", timestamp: 0 }];
	for (let i = 1; i <= n; i++) {
		messages.push({ role: "assistant", content: [{ type: "toolCall", id: `call_${i}`, name: "device_screen" }] });
		messages.push(screen(i, size));
	}
	return messages;
}

// The tool call ids whose image was sent as the placeholder.
function evicted(messages: any[]): string[] {
	return messages
		.filter((m) => m.role === "toolResult" && m.content.some((p: any) => p.text === PLACEHOLDER))
		.map((m) => m.toolCallId);
}

function imageBytes(messages: any[]): number {
	return messages
		.flatMap((m) => (Array.isArray(m.content) ? m.content : []))
		.filter((p: any) => p.type === "image")
		.reduce((sum: number, p: any) => sum + p.data.length, 0);
}

// Scenario: a Bot has read 30 screenshots of 1 MB: the request is over the 24 MB budget. The oldest go out as the
// placeholder, in a whole batch, the rest fit, the newest stay whole, and the messages pi holds are not changed.
test("a context over budget sends the oldest images as the placeholder", () => {
	const messages = chat(30);
	const before = structuredClone(messages);

	const sent = evict(messages, new Set())!;

	assert.deepEqual(messages, before, "the session's messages are not changed");
	assert.deepEqual(evicted(sent), ["call_1", "call_2", "call_3", "call_4", "call_5", "call_6", "call_7", "call_8"]);
	assert.ok(imageBytes(sent) <= BUDGET_BYTES);
	assert.equal(sent.length, messages.length);
	const first = sent.find((m: any) => m.toolCallId === "call_1");
	assert.deepEqual(first.content, [
		{ type: "text", text: "screen 1" },
		{ type: "text", text: PLACEHOLDER },
	], "the tool's text stays; its image becomes the placeholder");
});

// Scenario: a short chat is sent as it is.
test("a context under budget is sent unchanged", () => {
	assert.equal(evict(chat(10), new Set()), undefined);
});

// Scenario: the Bot reads screenshots so large that only two would fit. The newest three still go whole: the Bot
// needs the screen it acts on, and the gateway's 32 MiB still takes them.
test("the newest three images always stay", () => {
	const sent = evict(chat(12, 10 * MB), new Set())!;

	assert.deepEqual(evicted(sent), Array.from({ length: 9 }, (_, i) => `call_${i + 1}`));
	assert.equal(FLOOR, 3);
});

// Scenario: a photo the owner sent is never replaced; it counts against the budget, so the Bot's screenshots go
// first (Hermes's reserved uploads).
test("images a person sent stay and count against the budget", () => {
	const messages = chat(20);
	messages.splice(1, 0, {
		role: "user",
		content: [{ type: "image", data: "B".repeat(10 * MB), mimeType: "image/png" }],
		timestamp: 0,
	});

	const sent = evict(messages, new Set())!;

	assert.equal(sent[1].content[0].type, "image", "the owner's photo is sent");
	assert.equal(evicted(sent).length, BATCH);
	assert.ok(imageBytes(sent) <= BUDGET_BYTES);
});

// Scenario: the Bot keeps reading screenshots, one per call. What is evicted changes only by whole batches, never
// comes back, and the request always fits: between two batches the request's prefix is the same, so the provider's
// prompt cache holds.
test("batches are stable across calls", () => {
	const ext = { handler: undefined as any };
	imageEviction({ on: (_event: string, handler: any) => (ext.handler = handler) });
	const ctx = { sessionManager: { getSessionId: () => "cht_mine" } };

	const changes: number[] = [];
	let last: string[] = [];
	for (let n = 1; n <= 60; n++) {
		const result = ext.handler({ messages: chat(n) }, ctx);
		const now = result ? evicted(result.messages) : [];

		assert.deepEqual(now.slice(0, last.length), last, `call ${n}: what was evicted stays evicted`);
		assert.ok(imageBytes(result ? result.messages : chat(n)) <= BUDGET_BYTES, `call ${n}: the request fits`);
		if (now.length !== last.length) {
			assert.equal(now.length - last.length, BATCH, `call ${n}: a whole batch at a time`);
			changes.push(n);
		}
		last = now;
	}

	assert.deepEqual(changes, [25, 33, 41, 49, 57], "a batch every 8 screenshots once over the budget");
});

// Scenario: pi compacts, and the oldest screenshots leave the context. What was evicted before stays evicted
// although the shorter context would fit without it, so the cached prefix is not rewritten back.
test("an eviction holds after the context shrinks", () => {
	const retired = new Set<string>();
	evict(chat(30), retired);

	// call_1 to call_6 are summarized: the 24 screenshots left are 24 MB, which fits the budget by themselves.
	const afterCompaction = chat(30).filter((m: any, i: number) => i === 0 || i > 12);
	assert.equal(evict(afterCompaction, new Set()), undefined);

	const sent = evict(afterCompaction, retired)!;

	assert.deepEqual(evicted(sent), ["call_7", "call_8"]);
});

// Scenario: pi changes to another session; it starts with nothing evicted.
test("a new session starts clean", () => {
	const ext = { handler: undefined as any };
	imageEviction({ on: (_event: string, handler: any) => (ext.handler = handler) });

	let id = "one";
	const ctx = { sessionManager: { getSessionId: () => id } };
	assert.equal(evicted(ext.handler({ messages: chat(30) }, ctx).messages).length, BATCH);

	id = "two";
	assert.equal(ext.handler({ messages: chat(20) }, ctx), undefined);
});
