package userstore

import "context"

// ExtendSession is extendSession, for the window between a request's read of its session and its write (issue
// #996): a test plays a request that read the session before another's write or its revocation.
func (s *Store) ExtendSession(ctx context.Context, id string, now int64) (bool, error) {
	return s.extendSession(ctx, id, now)
}
