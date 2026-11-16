package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kl3inIT/hypomnema/internal/shared/workspacefs"
)

// The Bot's helper answers one question about DIR/<bot>, and refuses a name
// that is not a Bot's, so it reads no other folder.
func TestFilesServeReadsOnlyABotsFolder(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "coordinator"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "coordinator", "notes.md"), []byte("plan"), 0o600); err != nil {
		t.Fatal(err)
	}

	ask := func(question string) workspacefs.Result { return serve(t, root, question) }

	if res := ask(`{"bot":"coordinator","request":{"op":"read","path":"notes.md"}}`); res.File == nil ||
		res.File.Content != "plan" {
		t.Errorf("read: %+v", res)
	}

	for _, bot := range []string{"..", "../coordinator", "Coordinator", ""} {
		q, err := json.Marshal(BotFilesRequest{Bot: bot, Request: workspacefs.Request{Op: workspacefs.OpList}})
		if err != nil {
			t.Fatal(err)
		}

		if res := ask(string(q)); res.Error == nil || res.Error.Code != workspacefs.CodeInvalidPath {
			t.Errorf("bot %q: %+v", bot, res)
		}
	}

	err := files(t.Context(), []string{"serve", "--root", "relative"}, strings.NewReader("{}"), &bytes.Buffer{})
	if err == nil {
		t.Error("a relative root is refused")
	}
}

// Scenario (issue #606): the owner attaches a file in the first chat with a Bot made a moment ago, before pi has ever
// started for it. Its folder is not there; making the attachments folder makes it, as pi's launcher would, while a
// list before that still says the folder is not there.
func TestFilesServeMakesANewBotsFolderOnAChange(t *testing.T) {
	root := t.TempDir()

	ask := func(question string) workspacefs.Result { return serve(t, root, question) }

	if res := ask(`{"bot":"bot_new","request":{"op":"list"}}`); res.Error == nil ||
		res.Error.Code != workspacefs.CodeGone {
		t.Fatalf("list before: %+v", res)
	}

	if res := ask(`{"bot":"bot_new","request":{"op":"mkdir","path":"attachments"}}`); res.Error != nil {
		t.Fatalf("mkdir: %+v", res.Error)
	}

	if info, err := os.Stat(filepath.Join(root, "bot_new", "attachments")); err != nil || !info.IsDir() {
		t.Errorf("the attachments folder: %v", err)
	}
}

// serve asks `files serve --root root` one question.
func serve(t *testing.T, root, question string) workspacefs.Result {
	t.Helper()

	var out bytes.Buffer
	if err := files(t.Context(), []string{"serve", "--root", root}, strings.NewReader(question), &out); err != nil {
		t.Fatal(err)
	}

	var res workspacefs.Result
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatal(err)
	}

	return res
}
