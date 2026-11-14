---
name: images
description: >-
  Makes and edits images with Hypomnema's own image commands, through the owner's Codex and Antigravity
  subscriptions on the control plane: saves each image as a file in your folder and prints its path, which you then
  send into the chat. Use when the owner asks you to draw, make, generate or edit a picture, an illustration, a logo,
  a diagram as a picture, or to change a photo they gave you. Not for charts from data or for screenshots.
---

# Images through Hypomnema

Make and edit images with the `image` commands of the `hypomnema` binary. The control plane asks the owner's
subscriptions (Codex, then Antigravity, in the order the owner set) for you: no key, no account, nothing to log in
to. Each image takes a minute or so.

## The commands

```bash
hypomnema image generate <prompt>
hypomnema image generate <prompt> --size 1536x1024 --n 2 --out images/
hypomnema image edit <prompt> --image photo.png
hypomnema image edit <prompt> --image a.png --image b.png --out images/poster.png
hypomnema image providers
```

| Flag | What it does |
|---|---|
| `--size` | `1024x1024` (square), `1536x1024` (landscape), `1024x1536` (portrait), `auto`, or an aspect ratio such as `16:9`. |
| `--n` | How many images, 1 to 10; 1 when left out. |
| `--out` | A folder to save into, or a file name (its extension becomes the image's real one; several images are numbered). The current folder when left out. |
| `--image` | For `image edit`: an image file to change, or to use as a reference; once an image, up to 14. |
| `--model` | Only when the owner names a model: `gpt-image-2` (Codex), `gemini-3.1-flash-image` (Antigravity), or `codex/<model>`, `antigravity/<model>`. |
| `--provider` | `codex` or `antigravity`, only when the owner asks for one. |
| `--quality` | Codex's `low`, `medium` or `high`. `--image-size` is Antigravity's `1K`, `2K` or `4K`. |

The command prints each saved file's absolute path, one a line, then which provider and model made it. `--json`
prints the same as data.

## Sending the image

Save under your own folder, then put each image in your reply on a line of its own:

```text
MEDIA:images/poster.png
```

The path is relative to your folder, or absolute inside it. In a Lark chat the file follows your reply. In the web
chat the line is not turned into a picture yet: say the file's name, which the owner opens in the Files panel.

## How to use them

- Write the prompt as a full description: the subject, the style, the composition, the light, and any text that
  must appear in the image, word for word.
- To change an image the owner gave you, use `image edit` with that file; say what to keep as well as what to change.
- Make one image unless the owner asks for more: each one spends the owner's subscription.
- Look at what came back before you send it, when you can read images; make it again with a better prompt rather
  than sending a wrong one.

## When it fails

- "rate limited": every provider rests after its limit. The error says when to try again; tell the owner in one
  line and do not retry in a loop.
- "returned text instead of an image": the model refused, and its words follow. Tell the owner why, or change the
  prompt if the refusal was about something the owner did not ask for.
- "no image provider" or "no account": no subscription that makes images is logged in on the control plane. Say so;
  the owner adds one on the Models page.
- A model name that is wrong comes back with the provider's words and the name. Leave `--model` out.
