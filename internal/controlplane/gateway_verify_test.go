package controlplane

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kl3inIT/hypomnema/internal/controlplane/modelgateway"
	"github.com/kl3inIT/hypomnema/internal/controlplane/subscription"
)

// An account Google wants verified that is not due for its next try is resting to the gateway (issue #822), so its
// request goes to the next account rather than failing; any other error stays as it is.
func TestAnAccountToVerifyIsRestingToTheGateway(t *testing.T) {
	t.Parallel()

	marked := fmt.Errorf("opening account acc_1: %w",
		&subscription.VerificationError{URL: "https://accounts.google.com/x", RetryAt: time.Now().Add(time.Hour)})

	_, err := accountToken(subscription.Token{}, marked)
	require.ErrorIs(t, err, modelgateway.ErrAccountResting)

	other := errors.New("the vault is sealed")
	_, err = accountToken(subscription.Token{}, other)
	require.ErrorIs(t, err, other)
	assert.NotErrorIs(t, err, modelgateway.ErrAccountResting)
}
