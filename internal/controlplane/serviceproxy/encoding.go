package serviceproxy

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"strings"
)

// transferDecoded undoes a mail part's transfer encoding. A body that does
// not decode is kept as it is: it is shown and matched as sent.
func transferDecoded(encoding string, body []byte) []byte {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		compact := bytes.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}

			return r
		}, body)

		out, err := base64.StdEncoding.DecodeString(string(compact))
		if err != nil {
			return body
		}

		return out
	case "quoted-printable":
		out, err := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		if err != nil {
			return body
		}

		return out
	default:
		return body
	}
}

// base64URL decodes Gmail's "raw" field, padded or not.
func base64URL(text string) ([]byte, bool) {
	out, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(text, "="))
	if err != nil || len(out) == 0 {
		return nil, false
	}

	return out, true
}
