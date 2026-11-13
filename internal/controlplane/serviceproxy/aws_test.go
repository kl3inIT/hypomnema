package serviceproxy_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/shared/agenttool"
)

// The session the proxy signs with, which stands for a role's: made up.
const (
	roleKeyID   = "ASIAROLESESSION00001"
	roleSecret  = "the-role-session-secret"
	roleSession = "the-role-session-token"
)

// awsCall is a request as the AWS CLI sends one: signed with the placeholder
// key it is given, for a service and region.
func (tl *tool) awsCall(service, region string, req *http.Request, body []byte) answer {
	tl.t.Helper()

	sum := sha256.Sum256(body)
	creds := aws.Credentials{AccessKeyID: agenttool.Placeholder, SecretAccessKey: agenttool.Placeholder}
	require.NoError(tl.t, v4.NewSigner().SignHTTP(tl.t.Context(), creds, req, hex.EncodeToString(sum[:]), service,
		region, time.Now().UTC()))

	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	require.NoError(tl.t, req.Write(tl.conn))

	resp, err := http.ReadResponse(tl.reader, req)
	require.NoError(tl.t, err)

	raw, err := io.ReadAll(resp.Body)
	require.NoError(tl.t, err)
	require.NoError(tl.t, resp.Body.Close())

	return answer{Status: resp.StatusCode, Body: string(raw)}
}

func awsRequest(t *testing.T, method, host, contentType string, body []byte) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, "https://"+host+"/", bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", contentType)

	return req
}

const form = "application/x-www-form-urlencoded; charset=utf-8"

// Scenario: an agent given an AWS role reads the logs of the owner's server
// with the AWS CLI (issue #331). The CLI holds placeholders; the request
// reaches AWS signed with the role's session, for the service and region the
// CLI signed for, and nothing asks. An operation that makes credentials is
// never run; one the owner marked ask waits for him; a request the CLI did
// not sign, and a host that is not AWS's, are refused.
func TestAnAgentUsesAnAWSRoleWithoutHoldingItsSession(t *testing.T) {
	t.Parallel()

	w := newWorld(t, 100*time.Millisecond)
	ctx := t.Context()

	_, err := w.svc.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: "aws", Label: "main-read", Account: "123456789012", Secret: "integration-aws-main",
		Address: "arn:aws:iam::123456789012:role/hypomnema/hypomnema-read",
	})
	require.NoError(t, err)
	_, err = w.svc.SetConnectionChats(ctx, "aws", "main-read", true)
	require.NoError(t, err)

	const logs = "logs.ap-southeast-1.amazonaws.com"

	cli, refused := w.openTo("aws", w.pi, "main-read", logs, w.chat)
	require.Zero(t, refused)

	body := []byte(`{"logGroupName":"/beyondpilot/app","startTime":1763000000000}`)
	read := awsRequest(t, http.MethodPost, logs, "application/x-amz-json-1.1", body)
	read.Header.Set("X-Amz-Target", "Logs_20140328.FilterLogEvents")

	got := cli.awsCall("logs", "ap-southeast-1", read, body)
	require.Equal(t, http.StatusOK, got.Status, got.Body)
	assert.Empty(t, w.approvals(""), "what the role allows asks nobody")

	reached := w.service.received()
	require.Len(t, reached, 1)
	assert.Equal(t, body, reached[0].Body)
	assert.Contains(t, reached[0].Authorization, "Credential="+roleKeyID+"/")
	assert.Contains(t, reached[0].Authorization, "/ap-southeast-1/logs/aws4_request")
	assert.Equal(t, roleSession, reached[0].Header.Get("X-Amz-Security-Token"))
	assert.NotContains(t, reached[0].Authorization, agenttool.Placeholder)

	// The signature is the role's for the request as it was sent: signing it again at its time gives the same.
	at, err := time.Parse("20060102T150405Z", reached[0].Header.Get("X-Amz-Date"))
	require.NoError(t, err)

	check := awsRequest(t, http.MethodPost, logs, "", body)
	check.Header = reached[0].Header.Clone()
	check.Header.Del("Authorization")

	sum := sha256.Sum256(body)
	require.NoError(t, v4.NewSigner().SignHTTP(ctx, aws.Credentials{
		AccessKeyID: roleKeyID, SecretAccessKey: roleSecret, SessionToken: roleSession,
	}, check, hex.EncodeToString(sum[:]), "logs", "ap-southeast-1", at))
	assert.Equal(t, check.Header.Get("Authorization"), reached[0].Authorization)

	// Making credentials is never run.
	sts, refused := w.openTo("aws", w.pi, "main-read", "sts.amazonaws.com", w.chat)
	require.Zero(t, refused)

	assume := []byte("Action=AssumeRole&Version=2011-06-15&RoleArn=arn%3Aaws%3Aiam%3A%3A123456789012%3Arole%2Fadmin")
	got = sts.awsCall("sts", "us-east-1", awsRequest(t, http.MethodPost, "sts.amazonaws.com", form, assume), assume)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.message(), "never run")
	assert.Len(t, w.service.received(), 1, "nothing more reached AWS")

	// The owner marks one operation of this role ask.
	require.NoError(t, w.svc.SetActionPolicy(ctx, "aws", "main-read", "aws.ec2.TerminateInstances",
		orchestration.PolicyAsk))

	const ec2 = "ec2.ap-southeast-1.amazonaws.com"

	compute, refused := w.openTo("aws", w.pi, "main-read", ec2, w.chat)
	require.Zero(t, refused)

	terminate := []byte("Action=TerminateInstances&Version=2016-11-15&InstanceId.1=i-0151a7d0c30d7bc47")
	got = compute.awsCall("ec2", "ap-southeast-1", awsRequest(t, http.MethodPost, ec2, form, terminate), terminate)
	assert.Equal(t, http.StatusForbidden, got.Status)
	assert.Contains(t, got.message(), "Waiting for the owner's approval")
	assert.Equal(t, "aws.ec2.TerminateInstances", w.pending().Action)

	describe := []byte("Action=DescribeInstances&Version=2016-11-15")
	assert.Equal(t, http.StatusOK,
		compute.awsCall("ec2", "ap-southeast-1", awsRequest(t, http.MethodPost, ec2, form, describe), describe).Status)

	// A request with no signature of the CLI's is not signed here.
	unsigned := compute.do(http.MethodGet, "/?Action=DescribeRegions", "", nil)
	assert.Equal(t, http.StatusForbidden, unsigned.Status)

	_, refused = w.openTo("aws", w.pi, "main-read", "example.com", w.chat)
	assert.Equal(t, http.StatusForbidden, refused, "only AWS's hosts are reached")

	assert.False(t, strings.Contains(w.logs.String(), roleSecret) || strings.Contains(w.logs.String(), roleSession),
		"the log never holds the session")
}
