package serviceproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// AWS through the proxy (issue #331). The AWS CLI signs every request with
// the placeholder key it is given; the proxy reads the request's operation,
// answers it, drops that signature and signs the request again with the
// session of the role the agent was given, for the service and region the
// CLI itself signed for. awslabs' aws-sigv4-proxy signs what it forwards the
// same way (handler/proxy_client.go, Apache-2.0, read for behavior). The
// agent never holds a key, not even a session's.
//
// An AWS account has thousands of operations, so its catalog is not a list:
// a request is named by its service and operation, "aws.<service>.<Operation>"
// ("aws.ec2.TerminateInstances"), and what a role allows runs, nothing asking
// by default (owner, 2026-11-13). The owner may set ask or never on one
// operation of one role. The operations that make credentials are never,
// whatever is set: an agent that made a key would hold one. The list is
// awslabs aws-api-mcp-server's (core/metadata/read_only_operations_list.py,
// OVERRIDES, Apache-2.0).

// AWSID is the AWS Integration.
const AWSID = "aws"

// awsHost is a host of AWS's own: a service's endpoint, a bucket's, a
// region's. Every one is reached; what a role may do there is AWS's to say.
var awsHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+amazonaws\.com(\.cn)?$`)

// awsMinting are the operations that give credentials, by service.
var awsMinting = map[string][]string{
	"sts": {
		"AssumeRole", "AssumeRoleWithWebIdentity", "AssumeRoleWithSAML", "GetSessionToken", "GetFederationToken",
		"AssumeRoot",
	},
	"iam":              {"CreateAccessKey", "CreateLoginProfile", "UpdateLoginProfile", "CreateServiceSpecificCredential"},
	"cognito-identity": {"GetCredentialsForIdentity", "GetOpenIdToken"},
	"sso":              {"GetRoleCredentials"},
	"awsssoportal":     {"GetRoleCredentials"},
}

var (
	awsOperationPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
	awsServicePattern   = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
)

var awsCatalog = Catalog{
	Integration: AWSID,
	Reaches:     awsHost.MatchString,
	Signed:      true,
	Name:        awsAction,
	Known:       awsKnown,
}

// awsScope is the service and region a request was signed for, read from
// its signature's credential scope: Credential=KEY/DATE/REGION/SERVICE/aws4_request.
func awsScope(header http.Header) (region, service string, ok bool) {
	_, after, found := strings.Cut(header.Get("Authorization"), "Credential=")
	if !found {
		return "", "", false
	}

	scope, _, _ := strings.Cut(after, ",")
	parts := strings.Split(strings.TrimSpace(scope), "/")

	if len(parts) != 5 || parts[4] != "aws4_request" || parts[2] == "" || parts[3] == "" {
		return "", "", false
	}

	return parts[2], parts[3], true
}

// awsOperation is the operation a request asks for: the target of a JSON
// API, the Action of a query API, and for a REST API its method, which names
// what it does there.
func awsOperation(req *http.Request, body []byte) (operation string, rest bool) {
	if target := req.Header.Get("X-Amz-Target"); target != "" {
		_, op, found := strings.Cut(target, ".")
		if !found {
			op = target
		}

		return op, false
	}

	if action := req.URL.Query().Get("Action"); action != "" {
		return action, false
	}

	if strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if form, err := url.ParseQuery(string(body)); err == nil && form.Get("Action") != "" {
			return form.Get("Action"), false
		}
	}

	return req.Method, true
}

// awsAction names a request as an action.
func awsAction(req *http.Request, body []byte, host string) Action {
	_, service, signed := awsScope(req.Header)
	if !signed {
		return Action{
			ID: "aws.unsigned", Service: "AWS", Name: "A request the AWS CLI did not sign", Kind: NeedsCare, Never: true,
			Text: "Only what the AWS CLI signs is signed again",
		}
	}

	operation, rest := awsOperation(req, body)
	if !awsOperationPattern.MatchString(operation) {
		operation = "Unknown"
	}

	a := Action{
		ID: "aws." + service + "." + operation, Service: service, Name: service + " " + operation, Kind: Changes,
	}

	switch {
	case rest:
		a.Name = service + " " + req.Method + " " + req.URL.Path

		if req.Method == http.MethodGet || req.Method == http.MethodHead {
			a.Kind = Reads
		}
	case awsReads(operation):
		a.Kind = Reads
	}

	for _, minting := range awsMinting[service] {
		if operation == minting {
			a.Never, a.Kind = true, NeedsCare
			a.Text = "It makes credentials, which the agent would then hold"
		}
	}

	return a
}

// awsReads says whether an operation's name says it only reads, as AWS
// names them (Describe, Get, List…); it sorts the log, it decides nothing.
func awsReads(operation string) bool {
	for _, prefix := range []string{"Describe", "Get", "List", "Search", "Lookup", "Query", "Scan", "Filter",
		"Head", "Select", "Check", "BatchGet", "Test"} {
		if strings.HasPrefix(operation, prefix) {
			return true
		}
	}

	return false
}

// awsKnown names an action the owner sets an answer for, "aws.<service>.<Operation>",
// or for a REST API "aws.<service>.<METHOD>". One that makes credentials is
// never and takes no answer.
func awsKnown(id string) (Action, bool) {
	rest, ok := strings.CutPrefix(id, "aws.")
	if !ok {
		return Action{}, false
	}

	service, operation, ok := strings.Cut(rest, ".")
	if !ok || !awsServicePattern.MatchString(service) || !awsOperationPattern.MatchString(operation) {
		return Action{}, false
	}

	for _, minting := range awsMinting[service] {
		if operation == minting {
			return Action{}, false
		}
	}

	kind := Changes
	if awsReads(operation) || operation == http.MethodGet || operation == http.MethodHead {
		kind = Reads
	}

	return Action{ID: id, Service: service, Name: service + " " + operation, Kind: kind}, true
}

// AWSCredentials gives the session of a role of the AWS Integration.
type AWSCredentials func(ctx context.Context, integration, label string) (aws.Credentials, error)

// The payload hashes AWS takes in place of the body's own: a body sent
// unsigned, over TLS.
var awsUnsignedPayloads = []string{"UNSIGNED-PAYLOAD", "STREAMING-UNSIGNED-PAYLOAD-TRAILER"}

// signAWS signs a request again with a role's session, for the region and
// service the CLI signed it for. The CLI's signature is dropped first. A
// body signed in chunks with the CLI's key cannot be signed again as it
// passes, and is refused.
func signAWS(ctx context.Context, creds aws.Credentials, original, upstream *http.Request, body []byte) error {
	region, service, ok := awsScope(original.Header)
	if !ok {
		return errors.New("the request was not signed by the AWS CLI")
	}

	for _, name := range []string{"Authorization", "X-Amz-Date", "X-Amz-Security-Token"} {
		upstream.Header.Del(name)
	}

	payload := upstream.Header.Get("X-Amz-Content-Sha256")

	switch {
	case strings.HasPrefix(payload, "STREAMING-AWS4"):
		return fmt.Errorf("a body signed in chunks (%s) cannot be signed again; it was not sent", payload)
	case payload != "" && !slices.Contains(awsUnsignedPayloads, payload):
		// The hash of the body the CLI sent, computed here again: the body is what is sent.
		sum := sha256.Sum256(body)
		payload = hex.EncodeToString(sum[:])
		upstream.Header.Set("X-Amz-Content-Sha256", payload)
	case payload == "":
		sum := sha256.Sum256(body)
		payload = hex.EncodeToString(sum[:])
	}

	return v4.NewSigner().SignHTTP(ctx, creds, upstream, payload, service, region, time.Now().UTC())
}

// signAgain signs a request to AWS with the session of the Connection's
// role, and says why it was not, for the tool.
func (p *Proxy) signAgain(ctx context.Context, original, upstream *http.Request, body []byte, s session) string {
	if p.cfg.AWS == nil {
		return "this control plane has no AWS roles; the request was not run"
	}

	creds, err := p.cfg.AWS(ctx, s.integration, s.label)
	if err != nil {
		p.cfg.Logger.ErrorContext(ctx, "the proxy could not get a role's session",
			slog.String("event", "serviceproxy.failed"), slog.String("connection", s.connection()),
			slog.String("error", err.Error()))

		return "the role's session could not be had; the request was not run: " + err.Error()
	}

	if err := signAWS(ctx, creds, original, upstream, body); err != nil {
		return "Refused: " + err.Error()
	}

	return ""
}
