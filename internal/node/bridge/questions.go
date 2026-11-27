package bridge

import (
	"os"
	"path/filepath"
)

// EnableQuestionParts says the control plane that offered the Dispatch keeps
// a question's several decisions and its options' descriptions (issue
// #1219), so the worker's tools may take them; a control plane of an earlier
// release does not say it, and they take one question only.
func EnableQuestionParts(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "question-parts"), nil, 0o600)
}

// QuestionPartsEnabled reports whether the worker's tools may take several
// decisions in a question.
func QuestionPartsEnabled(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "question-parts"))

	return err == nil
}
