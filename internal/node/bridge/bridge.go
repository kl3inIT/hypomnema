// Package bridge is the mailbox between a worker's tools and the node
// daemon (issue #32): a directory beside the worker's journal, as
// Omnigent's `serve-mcp --bridge-dir` (omnigent/harnesses/claude_native/
// bridge.py at cf1021e). The worker's MCP server writes a question to
// asks/<n>.json and waits for answers/<n>.json; it writes its final report
// to finish.json. Files survive a daemon restart, so a question asked
// before the restart is still answered after it.
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kl3inIT/hypomnema/internal/shared/nodeproto"
)

// Dir is the bridge directory inside a worker's run directory.
func Dir(runDir string) string { return filepath.Join(runDir, "bridge") }

// Question is one question the worker asked.
type Question struct {
	Seq int           `json:"seq"`
	Ask nodeproto.Ask `json:"ask"`
}

// Final is the worker's final report through its finish tool.
type Final struct {
	Summary  string               `json:"summary"`
	Evidence []nodeproto.Evidence `json:"evidence,omitempty"`
	Gate     *nodeproto.Ask       `json:"gate,omitempty"`
}

func asks(dir string) string    { return filepath.Join(dir, "asks") }
func answers(dir string) string { return filepath.Join(dir, "answers") }

// Ask records a question and returns its sequence number.
func Ask(dir string, a nodeproto.Ask) (int, error) {
	if err := os.MkdirAll(asks(dir), 0o700); err != nil {
		return 0, err
	}

	raw, err := json.Marshal(a)
	if err != nil {
		return 0, err
	}

	for seq := int(time.Now().UnixNano() % 1e12); ; seq++ {
		f, err := os.OpenFile(filepath.Join(asks(dir), strconv.Itoa(seq)+".json"),
			os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}

		if err != nil {
			return 0, err
		}

		_, werr := f.Write(raw)

		return seq, errors.Join(werr, f.Close())
	}
}

// Questions lists the questions asked, oldest first.
func Questions(dir string) ([]Question, error) {
	entries, err := os.ReadDir(asks(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	var out []Question

	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}

		seq, err := strconv.Atoi(name)
		if err != nil {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(asks(dir), e.Name()))
		if err != nil {
			return nil, err
		}

		q := Question{Seq: seq}
		if err := json.Unmarshal(raw, &q.Ask); err != nil {
			return nil, fmt.Errorf("decoding question %d: %w", seq, err)
		}

		out = append(out, q)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })

	return out, nil
}

// Answer records the answer to a question, written whole.
func Answer(dir string, seq int, text string) error {
	return writeFile(answers(dir), strconv.Itoa(seq)+".json", map[string]string{"answer": text})
}

// Answered returns a question's answer once there is one.
func Answered(dir string, seq int) (string, bool, error) {
	raw, err := os.ReadFile(filepath.Join(answers(dir), strconv.Itoa(seq)+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}

	if err != nil {
		return "", false, err
	}

	var a struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", false, err
	}

	return a.Answer, true, nil
}

// Mark records a fact about a question (its report id, its question id), so
// a restarted daemon does not ask it twice.
func Mark(dir string, seq int, key, value string) error {
	return os.WriteFile(filepath.Join(asks(dir), fmt.Sprintf("%d.%s", seq, key)), []byte(value), 0o600)
}

// Marked returns a fact recorded with Mark.
func Marked(dir string, seq int, key string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(asks(dir), fmt.Sprintf("%d.%s", seq, key)))
	if err != nil {
		return "", false
	}

	return string(raw), true
}

// Finish records the worker's final report; a second call replaces the first.
func Finish(dir string, f Final) error { return writeFile(dir, "finish.json", f) }

// Finished returns the final report, if the worker made one.
func Finished(dir string) (Final, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "finish.json")) //nolint:gosec // the bridge's own file
	if errors.Is(err, os.ErrNotExist) {
		return Final{}, false, nil
	}

	if err != nil {
		return Final{}, false, err
	}

	var f Final
	if err := json.Unmarshal(raw, &f); err != nil {
		return Final{}, false, fmt.Errorf("decoding the final report: %w", err)
	}

	return f, true, nil
}

// Agent is where a worker's tools server reaches the control plane's agent
// API, and the Dispatch's own key to it (issue #212). The runner writes it
// before the worker starts. It is a file and not the server's environment so
// that every CLI gets it the same way and the key is on no command line.
type Agent struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

// SetAgent records the agent API of a Dispatch.
func SetAgent(dir string, a Agent) error { return writeFile(dir, "agent.json", a) }

// AgentOf returns the agent API of a Dispatch, if its runner recorded one.
func AgentOf(dir string) (Agent, bool, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "agent.json")) //nolint:gosec // the bridge's own file
	if errors.Is(err, os.ErrNotExist) {
		return Agent{}, false, nil
	}

	if err != nil {
		return Agent{}, false, err
	}

	var a Agent
	if err := json.Unmarshal(raw, &a); err != nil {
		return Agent{}, false, fmt.Errorf("decoding the agent API's address: %w", err)
	}

	return a, true, nil
}

func writeFile(dir, name string, v any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}

	tmp := filepath.Join(dir, name+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, filepath.Join(dir, name))
}
