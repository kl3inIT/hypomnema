package inboxstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/internal/store"
	"github.com/kl3inIT/hypomnema/internal/controlplane/orchestration/storeerr"
)

// ConnectionIntegration is the integration whose Connection the source names, or empty for other sources.
func (src Source) ConnectionIntegration() string {
	if src.Kind == FromGmail {
		return "google"
	}

	if namesAccount(src.Kind) {
		return src.Kind
	}

	return ""
}

// ConnectionLabelList formats the available labels for a refusal.
func ConnectionLabelList(labels []string) string {
	if len(labels) == 0 {
		return "none"
	}

	return strings.Join(labels, ", ")
}

// CheckSourceAccount checks that a named account still exists before sending a reply.
func (s *Store) CheckSourceAccount(ctx context.Context, src Source) error {
	return checkSourceAccount(ctx, store.New(s.db), src)
}

func checkSourceAccount(ctx context.Context, q *store.Queries, src Source) error {
	integration := src.ConnectionIntegration()
	if integration == "" || src.Account == "" {
		return nil
	}

	connections, err := q.ListConnectionsOf(ctx, integration)
	if err != nil {
		return fmt.Errorf("listing the connections of %s: %w", integration, err)
	}

	labels := make([]string, 0, len(connections))
	for _, c := range connections {
		if c.Label == src.Account {
			return nil
		}

		labels = append(labels, c.Label)
	}

	return fmt.Errorf("%w: this item's account %q is not a %s Connection; the Connections are: %s",
		storeerr.ErrInvalid, src.Account, integration, ConnectionLabelList(labels))
}
