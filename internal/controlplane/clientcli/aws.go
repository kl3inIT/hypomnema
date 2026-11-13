package clientcli

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
)

// integrationsConnectRole connects a role of an AWS account (issue #331).
// The key is read from a file and is never an argument: arguments show in the
// shell's history and in the process list. Without a file, the key kept under
// that name assumes the role.
func integrationsConnectRole(ctx context.Context, e *env, fs *flag.FlagSet, id string) error {
	label, role, key := str(fs, "label"), str(fs, "role"), str(fs, "key")
	if role == "" {
		return fmt.Errorf("pass --role with the role's ARN, --key with the name of the key that assumes it, and "+
			"--key-file the first time that key is used (%s)", id)
	}

	body := openapi.NewAccountConnection{RoleArn: &role, Key: &key}

	if file := str(fs, "key-file"); file != "" {
		raw, err := e.read(file)
		if err != nil {
			return err
		}

		keyID, secret, err := parseAccessKey(raw)
		if err != nil {
			return err
		}

		body.AccessKeyId, body.SecretAccessKey = &keyID, &secret
	}

	resp, err := e.api.PutIntegrationConnectionWithResponse(ctx, id, label, body)
	if err != nil {
		return fmt.Errorf("connecting %s:%s: %w", id, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("connecting "+id+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	c := resp.JSON200

	return e.print(fs, c, func() {
		e.printf("%s:%s is the role %s of account %s, assumed with key %s\n", id, c.Label, val(c.RoleArn), c.Account,
			val(c.Key))
	})
}

// parseAccessKey reads an access key as the AWS console gives it, a .csv with
// the columns "Access key ID" and "Secret access key", or as one line
// ACCESS_KEY_ID:SECRET_ACCESS_KEY.
func parseAccessKey(raw string) (id, secret string, err error) {
	// The console's file starts with a byte order mark.
	raw = strings.TrimSpace(strings.TrimPrefix(raw, string(rune(0xFEFF))))

	if id, secret, ok := strings.Cut(raw, ":"); ok && !strings.ContainsAny(raw, ",\n") {
		return strings.TrimSpace(id), strings.TrimSpace(secret), nil
	}

	rows, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err == nil && len(rows) == 2 {
		idAt, secretAt := -1, -1

		for i, name := range rows[0] {
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "access key id":
				idAt = i
			case "secret access key":
				secretAt = i
			}
		}

		if idAt >= 0 && secretAt >= 0 && idAt < len(rows[1]) && secretAt < len(rows[1]) {
			return strings.TrimSpace(rows[1][idAt]), strings.TrimSpace(rows[1][secretAt]), nil
		}
	}

	return "", "", errors.New("the key file is the .csv the AWS console gives for an access key, or one line " +
		"ACCESS_KEY_ID:SECRET_ACCESS_KEY")
}
