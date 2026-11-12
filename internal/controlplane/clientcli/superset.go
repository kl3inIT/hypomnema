package clientcli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/clientapi/openapi"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// Superset from the terminal (issue #203): a dashboard's charts read as
// data, and any other request to a Connection's API. The commands are the
// shape of sup's (dashboard list, chart data) and `superset api` of treg's
// `treg call`: it relays one request and prints the answer as it came. The
// control plane signs in and adds the token; nothing here holds one.

const supersetID = "superset"

func supersetFlags(fs *flag.FlagSet) {
	fs.String("connection", "", "the Superset account's label (default: the only one connected)")
}

// supersetLabel is the Connection a command is for: the one named, or the
// only one there is. With several and none named nothing is guessed.
func supersetLabel(ctx context.Context, e *env, fs *flag.FlagSet) (string, error) {
	if label := str(fs, "connection"); label != "" {
		return label, nil
	}

	service, err := serviceOf(ctx, e, supersetID)
	if err != nil {
		return "", err
	}

	if service == nil || len(service.Connections) == 0 {
		return "", errors.New("no Superset account is connected; the owner connects one with `hypomnema integrations " +
			"connect superset --label L --url URL --user NAME --password-file FILE`")
	}

	if len(service.Connections) > 1 {
		labels := make([]string, 0, len(service.Connections))
		for _, c := range service.Connections {
			labels = append(labels, c.Label)
		}

		return "", fmt.Errorf("several Superset accounts are connected (%s); name one with --connection",
			strings.Join(labels, ", "))
	}

	return service.Connections[0].Label, nil
}

// askingChat is the chat a coordinator runs the command in, so that an
// Approval its request waits for is answered there.
func askingChat() *string {
	if chat := os.Getenv(chatEnv); chat != "" {
		return &chat
	}

	return nil
}

// askingRun is the key of the Routine's run whose script runs the command,
// so that an Approval its request waits for belongs to that run.
func askingRun() *string {
	if key := agenttool.RoutineRunKey(); key != "" {
		return &key
	}

	return nil
}

// integrationsConnectAccount connects an Integration that is an account at
// an address of its own. The password is read from a file and is never an
// argument: arguments show in the shell's history and in the process list.
func integrationsConnectAccount(ctx context.Context, e *env, fs *flag.FlagSet, id string) error {
	label, address, user, file := str(fs, "label"), str(fs, "url"), str(fs, "user"), str(fs, "password-file")
	if address == "" || user == "" || file == "" {
		return fmt.Errorf("pass --url with %s's address, --user with the account's user name and --password-file "+
			"with the file holding its password, or - to read it from standard input", id)
	}

	password, err := e.read(file)
	if err != nil {
		return err
	}

	password = strings.TrimRight(password, "\r\n")

	resp, err := e.api.PutIntegrationConnectionWithResponse(ctx, id, label, openapi.NewAccountConnection{
		Address: &address, Username: &user, Password: &password,
	})
	if err != nil {
		return fmt.Errorf("connecting %s:%s: %w", id, label, err)
	}

	if resp.JSON200 == nil {
		return apiError("connecting "+id+":"+label, resp.StatusCode(), resp.JSONDefault)
	}

	c := resp.JSON200

	return e.print(fs, c, func() {
		version := ""
		if v := val(c.Version); v != "" {
			version = ", version " + v
		}

		e.printf("%s:%s is connected as %s at %s%s\n", id, c.Label, c.Account, val(c.Address), version)
	})
}

func supersetDashboards(ctx context.Context, e *env, fs *flag.FlagSet) error {
	label, err := supersetLabel(ctx, e, fs)
	if err != nil {
		return err
	}

	resp, err := e.api.ListSupersetDashboardsWithResponse(ctx, label, &openapi.ListSupersetDashboardsParams{
		Q: optional(fs, "search"), ChatId: askingChat(), HypomnemaRoutineRun: askingRun(),
	})
	if err != nil {
		return fmt.Errorf("listing the dashboards of superset:%s: %w", label, err)
	}

	if resp.JSON200 == nil {
		return apiError("listing the dashboards of superset:"+label, resp.StatusCode(), resp.JSONDefault)
	}

	return e.print(fs, resp.JSON200, func() {
		if len(resp.JSON200.Data) == 0 {
			e.printf("no dashboard\n")
		}

		for _, d := range resp.JSON200.Data {
			e.printf("%d\t%s\n", d.Id, d.Title)
		}
	})
}

func supersetDashboard(ctx context.Context, e *env, fs *flag.FlagSet) error {
	label, err := supersetLabel(ctx, e, fs)
	if err != nil {
		return err
	}

	resp, err := e.api.GetSupersetDashboardWithResponse(ctx, label, fs.Arg(0), &openapi.GetSupersetDashboardParams{
		ChatId: askingChat(), HypomnemaRoutineRun: askingRun(),
	})
	if err != nil {
		return fmt.Errorf("reading dashboard %s of superset:%s: %w", fs.Arg(0), label, err)
	}

	if resp.JSON200 == nil {
		return apiError("reading dashboard "+fs.Arg(0)+" of superset:"+label, resp.StatusCode(), resp.JSONDefault)
	}

	d := resp.JSON200

	return e.print(fs, d, func() {
		e.printf("%d\t%s\n\ncharts:\n", d.Id, d.Title)

		for _, c := range d.Charts {
			note := ""
			if !c.Read {
				note = "\t(not read yet unless it has a saved query)"
			}

			dataset := val(c.Dataset)
			if dataset == "" {
				dataset = "dataset " + strconv.FormatInt(c.DatasetId, 10)
			}

			e.printf("  %d\t%s\t%s\t%s%s\n", c.Id, c.Name, c.Kind, dataset, note)
		}

		if len(d.Filters) == 0 {
			return
		}

		e.printf("\nfilters, with the value each has when the dashboard opens:\n")

		for _, f := range d.Filters {
			value := "none"
			if f.Default != nil {
				if raw, err := json.Marshal(f.Default); err == nil {
					value = string(raw)
				}
			}

			e.printf("  %s\t%s\t%s\t%s\n", f.Name, f.Kind, val(f.Column), value)
		}
	})
}

// supersetChart prints a chart's rows as its dashboard shows them: one JSON
// object with the columns, the rows, the SQL and when the figures were
// computed, or the rows as CSV, or with --sql the query alone.
func supersetChart(ctx context.Context, e *env, fs *flag.FlagSet) error {
	label, err := supersetLabel(ctx, e, fs)
	if err != nil {
		return err
	}

	chart, err := strconv.ParseInt(fs.Arg(1), 10, 64)
	if err != nil || chart <= 0 {
		return fmt.Errorf("%w: a chart is named by its number", ErrUsage)
	}

	format := openapi.GetSupersetChartDataParamsFormat(str(fs, "format"))
	if format != openapi.Json && format != openapi.Csv {
		return fmt.Errorf("%w: --format is json or csv", ErrUsage)
	}

	wantSQL := boolean(fs, "sql")
	if wantSQL {
		format = openapi.Json
	}

	doing := fmt.Sprintf("reading chart %d of dashboard %s of superset:%s", chart, fs.Arg(0), label)

	resp, err := e.api.GetSupersetChartDataWithResponse(ctx, label, fs.Arg(0), chart, &openapi.GetSupersetChartDataParams{
		Format: &format, ChatId: askingChat(), HypomnemaRoutineRun: askingRun(),
	})
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}

	if resp.StatusCode() != 200 {
		return apiError(doing, resp.StatusCode(), resp.JSONDefault)
	}

	if wantSQL && resp.JSON200 != nil {
		_, err = fmt.Fprintln(e.out, resp.JSON200.Sql)

		return err
	}

	// The answer is printed as it came: JSON on one line, or the CSV.
	_, err = e.out.Write(resp.Body)

	return err
}

// supersetAPI relays one request to Superset's API and prints the answer's
// body as it came. An answer that is not a success is printed too, and the
// command fails with its status.
func supersetAPI(ctx context.Context, e *env, fs *flag.FlagSet) error {
	label, err := supersetLabel(ctx, e, fs)
	if err != nil {
		return err
	}

	method := openapi.SupersetRequestMethod(strings.ToUpper(fs.Arg(0)))

	switch method {
	case openapi.GET, openapi.POST, openapi.PUT, openapi.PATCH, openapi.DELETE:
	default:
		return fmt.Errorf("%w: the method is GET, POST, PUT, PATCH or DELETE", ErrUsage)
	}

	request := openapi.SupersetRequest{Method: method, Path: fs.Arg(1), ChatId: askingChat()}

	if file := str(fs, "data"); file != "" {
		body, err := e.read(file)
		if err != nil {
			return err
		}

		request.Body = &body
	}

	doing := fmt.Sprintf("%s %s on superset:%s", method, fs.Arg(1), label)

	resp, err := e.api.RelaySupersetRequestWithResponse(ctx, label,
		&openapi.RelaySupersetRequestParams{HypomnemaRoutineRun: askingRun()}, request)
	if err != nil {
		return fmt.Errorf("%s: %w", doing, err)
	}

	if resp.JSON200 == nil {
		return apiError(doing, resp.StatusCode(), resp.JSONDefault)
	}

	answer := resp.JSON200

	if boolean(fs, "json") {
		return e.print(fs, answer, func() {})
	}

	body := []byte(answer.Body)
	if answer.Base64 {
		if body, err = base64.StdEncoding.DecodeString(answer.Body); err != nil {
			return fmt.Errorf("%s: the answer is not base64", doing)
		}
	}

	if _, err := e.out.Write(body); err != nil {
		return err
	}

	if len(body) > 0 && body[len(body)-1] != '\n' {
		_, _ = fmt.Fprintln(e.out)
	}

	if answer.Status < 200 || answer.Status > 299 {
		return fmt.Errorf("%s: Superset answered %d", doing, answer.Status)
	}

	return nil
}
