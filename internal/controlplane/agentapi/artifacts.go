package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
	"github.com/kl3inIT/hypomnema/internal/shared/artifactpack"
)

// show_artifact (issue #536) is one tool for a Bot's pi and a worker, after
// OpenMausBot's show_html (server/drivers/agents-app-tools.ts: html or path,
// a title, app_id to replace; its answer "Shown as app X. To change it, call
// show_html again with app_id", agents-call-cards.ts) and OpenClaw's
// show_widget (src/canvas/widget-tool.ts), each of which shows a page in the
// conversation the agent works for. A Bot's pi shows it in its own chat, as
// `hypomnema artifact show` does; a worker in its Run's chat, as that chat's
// Bot, so that either may revise the other's (orchestration's reviseArtifact:
// the same Bot and chat, OpenMausBot's per-conversation pages). A worker whose
// Run has no chat is refused and told to name the file in its finish summary
// (inference: neither reference has a sub-agent without a conversation).
// Neither reference gives an agent a tool to list or read pages, and nor does
// this: the CLI's artifact commands stay for the owner and Routine scripts.
//
// The files are read by the agent's tools server on its machine and sent
// whole (agenttool.ArtifactCall), and checked here again by the rules the
// client API checks by (artifactpack.Check). A tools server of an earlier
// release sends the agent's arguments as they are: html works, a path does
// not.

// showsArtifacts says whether the agent is given show_artifact: a worker and
// a Bot's pi in its chat. The owner's token, the coordinator token and a
// Routine's script have no MCP and keep the CLI.
func showsArtifacts(a agent) bool {
	return a.dispatchID != "" || a.chatID != "" && a.bot != ""
}

// artifactTool is show_artifact as an agent of its kind is offered it.
func artifactTool(a agent) (agenttool.BrowserTool, bool) {
	if !showsArtifacts(a) {
		return agenttool.BrowserTool{}, false
	}

	where := "in this chat"
	if a.dispatchID != "" {
		where = "in the chat your Run came from"
	}

	raw, err := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string",
				"description": "One line, at most 80 characters; a new artifact needs one."},
			"html": map[string]any{"type": "string", "description": "One HTML document, at most 256 KB."},
			"path": map[string]any{"type": "string", "description": "Instead of html: a file, or a folder with " +
				"index.html at its top, in the folder you work in."},
			"id": map[string]any{"type": "string",
				"description": "Only to change an artifact you showed: the id it returned."},
		},
	})
	if err != nil {
		return agenttool.BrowserTool{}, false
	}

	return agenttool.BrowserTool{
		Name: agenttool.ToolShowArtifact,
		Description: "Show the owner a page, a PDF, an image or any file " + where + ", as an artifact: a card he " +
			"opens beside the chat, full screen or on its own page; the result gives its id. Use it for something " +
			"he should look at (a page, a dashboard, a chart, a diagram); for a page or a dashboard, load the " +
			"pages skill first. A file to download is not an artifact. Send html, one self-contained HTML document of " +
			"at most 256 KB, or path, a file or a folder in the folder you work in; a folder has index.html at its " +
			"top and beside it only what the page loads (styles, scripts, data, images, fonts), at most 5 MB and " +
			"200 files. A page runs in a locked frame: no network requests, cookies or storage; scripts, styles " +
			"and fonts load only from cdnjs.cloudflare.com, cdn.jsdelivr.net, unpkg.com and Google Fonts, images " +
			"are data: URLs or come from those CDNs; embed the data it needs. Never put a secret in it. To change " +
			"an artifact you showed, or one a Bot or a worker of the same chat showed, call this again with its " +
			"id and the new html or path instead of showing a second one: the card shows the next revision in " +
			"place.",
		InputSchema: raw,
	}, true
}

// artifactFiles are the files a call shows, or what the agent must change.
func artifactFiles(in agenttool.ArtifactCall) ([]artifactpack.File, string) {
	switch {
	case in.HTML != "" && (in.Path != "" || len(in.Files) > 0):
		return nil, "give html or path, not both"
	case in.HTML != "":
		return []artifactpack.File{{Path: artifactpack.Entry, Data: []byte(in.HTML)}}, ""
	case len(in.Files) > 0:
		files := make([]artifactpack.File, 0, len(in.Files))
		for _, f := range in.Files {
			files = append(files, artifactpack.File{Path: f.Path, Data: f.Data})
		}

		return files, ""
	case in.Path != "":
		// A tools server of an earlier release sends the path without reading it.
		return nil, "this node's tools cannot send a folder yet; send html, or wait for its update"
	default:
		return nil, "give html, one HTML document, or path, a file or folder in the folder you work in"
	}
}

// artifactMaker is who the agent shows an artifact as: a Bot's pi its Bot in
// its chat, a worker its Run's chat's Bot in that chat.
func (s *Server) artifactMaker(ctx context.Context, a agent) (orchestration.ArtifactMaker, error) {
	if a.dispatchID == "" {
		return orchestration.ArtifactMaker{Bot: a.bot, Chat: a.chatID}, nil
	}

	return s.store.ArtifactMakerOfDispatch(ctx, a.dispatchID)
}

// artifactCall runs show_artifact and answers with the tool's result as MCP
// has it. What the agent can fix (the files, the title, an id not of its
// chat, a Run without a chat) is its result, marked as an error.
func (s *Server) artifactCall(w http.ResponseWriter, r *http.Request, a agent, call agenttool.BrowserCall) {
	var in agenttool.ArtifactCall
	if json.Unmarshal(orEmpty(call.Arguments), &in) != nil {
		answerTool(w, "show_artifact takes {title, html, path, id}", true)

		return
	}

	files, fix := artifactFiles(in)
	if fix != "" {
		answerTool(w, fix, true)

		return
	}

	pack, err := artifactpack.Check(files)
	if err != nil {
		answerTool(w, err.Error(), true)

		return
	}

	by, err := s.artifactMaker(r.Context(), a)

	switch {
	case errors.Is(err, orchestration.ErrNoChat):
		answerTool(w, "Your Run has no chat to show an artifact in: name the file's path in your finish summary "+
			"instead.", true)

		return
	case err != nil:
		s.fail(w, r, err)

		return
	}

	art, changed, err := s.store.ShowArtifact(r.Context(), by,
		orchestration.ShowArtifact{ID: in.ID, Title: in.Title, Pack: pack})

	switch {
	case err == nil:
	case errors.Is(err, orchestration.ErrInvalid) || errors.Is(err, orchestration.ErrNotFound) ||
		errors.Is(err, orchestration.ErrForbidden):
		answerTool(w, err.Error(), true)

		return
	default:
		s.fail(w, r, err)

		return
	}

	s.logger.InfoContext(r.Context(), "an agent showed an artifact",
		slog.String("event", "agentapi.artifact"), slog.String("agent", a.name), slog.String("artifact", art.ID),
		slog.Int("revision", art.Revision), slog.Bool("changed", changed))

	text := fmt.Sprintf("Shown as artifact %s (revision %d). To change it, call show_artifact again with id %s.",
		art.ID, art.Revision, art.ID)
	if !changed {
		text = fmt.Sprintf("Artifact %s is still at revision %d: its files and title are unchanged, so nothing new "+
			"was shown.", art.ID, art.Revision)
	}

	if a.dispatchID != "" {
		text += fmt.Sprintf(" Name artifact %s in your finish summary.", art.ID)
	}

	answerTool(w, text, false)
}
