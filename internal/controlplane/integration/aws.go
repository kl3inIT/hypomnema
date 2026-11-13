package integration

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration"
	"github.com/kl3inIT/hypomnema/internal/controlplane/vault"
)

// An Integration whose Connections are roles of the owner's AWS accounts
// (KindRole, issue #331). The owner enters a key once, the access key of an
// IAM user that may do nothing but assume roles; each Connection is one role,
// with the rights its work needs, assumed with that key. The control plane
// keeps each role's session in memory and renews it before it ends, as
// Omnara does (internal/sigv4/sigv4.go: stscreds in aws.NewCredentialsCache)
// and Coder's AI Bridge (aibridge/provider/bedrock.go): the key and the
// sessions never leave the control plane. Several keys may be kept, one an
// AWS account or one for several accounts whose roles trust it.

const (
	// awsSession is how long a role's session lasts (owner, 2026-11-13): the
	// longest STS gives a role assumed straight from a user, which the role's
	// own maximum must allow.
	awsSession = 12 * time.Hour
	// awsRenewBefore is how long before its end a session is renewed
	// (Omnara's ExpiryWindow).
	awsRenewBefore = 30 * time.Second
	// awsSessionName is the name STS records a session under; it asks for
	// one.
	awsSessionName = "hypomnema"
	// awsSTSRegion is where the role is assumed: STS answers in every region
	// for any role, and this one is never off.
	awsSTSRegion = "us-east-1"
)

var (
	awsRolePattern  = regexp.MustCompile(`^arn:aws(-cn|-us-gov)?:iam::(\d{12}):role/[\w+=,.@/-]{1,512}$`)
	awsKeyIDPattern = regexp.MustCompile(`^[A-Z0-9]{16,128}$`)
)

// Role is what the owner gives to connect a role: the ARN of the role, the
// name of the key that assumes it, and that key when it is new or replaced.
type Role struct {
	ARN string
	// Key names the key: lower-case letters, digits and dashes.
	Key string
	// AccessKeyID and SecretAccessKey are the key's value; both empty to use
	// the key as it is kept.
	AccessKeyID     string
	SecretAccessKey string
}

// awsRole is the session of one role kept in memory, with the key it was
// assumed with.
type awsRole struct {
	secret string
	arn    string
	cache  *aws.CredentialsCache
}

func awsKeySecretName(key string) string { return "integration-aws-" + key }

// AWSKeyOf is the name of the key a Connection's secret is.
func AWSKeyOf(secret string) string { return strings.TrimPrefix(secret, "integration-aws-") }

// ConnectRole records a role as a Connection under a label, after assuming it
// once with the key: that shows the key and the role's trust work. A refused
// assume stores nothing. A key given is kept, replacing the one of its name,
// and every role assumed with the old one is assumed again.
func (s *Service) ConnectRole(ctx context.Context, integration, label string, r Role) (Connection, error) {
	d, err := s.definition(integration)
	if err != nil {
		return Connection{}, err
	}

	if !d.IsRole() {
		return Connection{}, fmt.Errorf("%w: %s is not connected with a role", ErrInvalid, d.ID)
	}

	if !orchestration.ValidConnectionLabel(label) {
		return Connection{}, fmt.Errorf("%w: a label is lower-case letters, digits and dashes, such as work", ErrInvalid)
	}

	arn := strings.TrimSpace(r.ARN)

	match := awsRolePattern.FindStringSubmatch(arn)
	if match == nil {
		return Connection{}, fmt.Errorf("%w: a role is its ARN, such as arn:aws:iam::123456789012:role/hypomnema/read",
			ErrInvalid)
	}

	if !orchestration.ValidConnectionLabel(r.Key) {
		return Connection{}, fmt.Errorf("%w: a key is named with lower-case letters, digits and dashes, such as main",
			ErrInvalid)
	}

	secret := awsKeySecretName(r.Key)

	given := r.AccessKeyID != "" || r.SecretAccessKey != ""

	var key string

	if given {
		id, secretKey := strings.TrimSpace(r.AccessKeyID), strings.TrimSpace(r.SecretAccessKey)
		if !awsKeyIDPattern.MatchString(id) || secretKey == "" || len(secretKey) > maxClientBytes ||
			strings.ContainsAny(secretKey, " \t\r\n:") {
			return Connection{}, fmt.Errorf("%w: a key is an access key id and its secret access key", ErrInvalid)
		}

		key = id + ":" + secretKey
	} else {
		opened, err := s.vault.Open(ctx, secret)
		if errors.Is(err, vault.ErrNotFound) {
			return Connection{}, fmt.Errorf("%w: there is no key %q yet; give its access key id and secret access key",
				ErrInvalid, r.Key)
		}

		if err != nil {
			return Connection{}, vaultError(err)
		}

		key = opened.Payload[vault.KeyValue]
	}

	cache, err := s.assumeRole(key, arn)
	if err != nil {
		return Connection{}, err
	}

	if _, err := cache.Retrieve(ctx); err != nil {
		return Connection{}, fmt.Errorf("%w: AWS did not let key %s assume %s: %s", ErrSignInFailed, r.Key, arn,
			awsCause(err))
	}

	if given {
		payload := vault.Payload{vault.KeyValue: key}

		_, err = s.vault.Create(ctx, vault.NewSecret{
			Name: secret, Kind: vault.KindGeneric, Payload: payload,
			Metadata: map[string]string{"integration": d.ID, "key": r.Key, "what": "an access key that assumes roles"},
		})
		if errors.Is(err, vault.ErrConflict) {
			_, err = s.vault.Rotate(ctx, secret, payload)
		}

		if err != nil {
			return Connection{}, vaultError(err)
		}

		// The roles assumed with the old key are assumed again with this one.
		s.dropRoles(secret)
	}

	saved, err := s.store.SaveServiceConnection(ctx, orchestration.Connection{
		Integration: d.ID, Label: label, Account: match[2], Secret: secret, Address: arn,
	})
	if err != nil {
		return Connection{}, err
	}

	s.mu.Lock()
	s.roles[accountKey(d.ID, label)] = awsRole{secret: secret, arn: arn, cache: cache}
	s.mu.Unlock()

	return s.view(ctx, d, saved), nil
}

// assumeRole is the session of a role assumed with a key, renewed before it
// ends.
func (s *Service) assumeRole(key, arn string) (*aws.CredentialsCache, error) {
	id, secret, ok := strings.Cut(key, ":")
	if !ok || id == "" || secret == "" {
		return nil, fmt.Errorf("%w: the key kept for this role is not an access key id and its secret", ErrInvalid)
	}

	options := sts.Options{
		Region: awsSTSRegion, Credentials: credentials.NewStaticCredentialsProvider(id, secret, ""),
		HTTPClient: s.client,
	}
	if s.stsEndpoint != "" {
		options.BaseEndpoint = aws.String(s.stsEndpoint)
	}

	provider := stscreds.NewAssumeRoleProvider(sts.New(options), arn, func(o *stscreds.AssumeRoleOptions) {
		o.Duration = awsSession
		o.RoleSessionName = awsSessionName
	})

	return aws.NewCredentialsCache(provider, func(o *aws.CredentialsCacheOptions) {
		o.ExpiryWindow = awsRenewBefore
	}), nil
}

// RoleCredentials gives a role's session, renewed when it is about to end.
// A role AWS no longer lets the key assume marks its Connection as needing to
// be connected again.
func (s *Service) RoleCredentials(ctx context.Context, integration, label string) (aws.Credentials, error) {
	d, err := s.definition(integration)
	if err != nil {
		return aws.Credentials{}, err
	}

	if !d.IsRole() {
		return aws.Credentials{}, fmt.Errorf("%w: %s has no roles", ErrInvalid, d.ID)
	}

	conn, err := s.store.ConnectionOf(ctx, d.ID, label)
	if err != nil {
		return aws.Credentials{}, err
	}

	role, err := s.role(ctx, d, conn)
	if err != nil {
		return aws.Credentials{}, err
	}

	creds, err := role.cache.Retrieve(ctx)
	if err != nil {
		s.dropRole(d.ID, label)
		_ = s.store.SetConnectionSignInFailed(ctx, d.ID, label, true)

		return aws.Credentials{}, fmt.Errorf("%w: %s:%s: AWS did not let its key assume %s: %s", ErrSignInRequired,
			d.ID, label, conn.Address, awsCause(err))
	}

	if conn.SignInFailed {
		_ = s.store.SetConnectionSignInFailed(ctx, d.ID, label, false)
	}

	return creds, nil
}

// RoleProvider is a role's session as the AWS SDK takes credentials, for the
// control plane's own use of a role: the machine provider's.
func (s *Service) RoleProvider(integration, label string) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		return s.RoleCredentials(ctx, integration, label)
	})
}

// role is the kept session of a Connection, or a new one from its key.
func (s *Service) role(ctx context.Context, d Definition, conn orchestration.Connection) (awsRole, error) {
	s.mu.Lock()
	kept, ok := s.roles[accountKey(d.ID, conn.Label)]
	s.mu.Unlock()

	if ok && kept.secret == conn.Secret && kept.arn == conn.Address {
		return kept, nil
	}

	opened, err := s.vault.Open(ctx, conn.Secret)
	if err != nil {
		return awsRole{}, vaultError(err)
	}

	cache, err := s.assumeRole(opened.Payload[vault.KeyValue], conn.Address)
	if err != nil {
		return awsRole{}, err
	}

	role := awsRole{secret: conn.Secret, arn: conn.Address, cache: cache}

	s.mu.Lock()
	s.roles[accountKey(d.ID, conn.Label)] = role
	s.mu.Unlock()

	return role, nil
}

func (s *Service) dropRole(integration, label string) {
	s.mu.Lock()
	delete(s.roles, accountKey(integration, label))
	s.mu.Unlock()
}

// dropRoles forgets the sessions assumed with a key.
func (s *Service) dropRoles(secret string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, r := range s.roles {
		if r.secret == secret {
			delete(s.roles, k)
		}
	}
}

// forgetRole removes a role's Connection and its session; its key goes too
// when no other role is assumed with it. A session already given ends on its
// own.
func (s *Service) forgetRole(ctx context.Context, d Definition, conn orchestration.Connection) error {
	if _, err := s.store.RemoveConnection(ctx, d.ID, conn.Label); err != nil {
		return err
	}

	s.dropRole(d.ID, conn.Label)

	others, err := s.store.ConnectionsOf(ctx, d.ID)
	if err != nil {
		return err
	}

	for _, c := range others {
		if c.Secret == conn.Secret {
			return nil
		}
	}

	if err := s.vault.Delete(ctx, conn.Secret); err != nil && !errors.Is(err, vault.ErrNotFound) {
		return vaultError(err)
	}

	return nil
}

// awsCause is what AWS said, without the request: its error code and message.
func awsCause(err error) string {
	var api interface {
		ErrorCode() string
		ErrorMessage() string
	}

	if errors.As(err, &api) {
		return api.ErrorCode() + ": " + api.ErrorMessage()
	}

	return rootCause(err)
}

func rootCause(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return err.Error()
		}

		err = next
	}
}
