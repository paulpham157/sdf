package server

import "context"

// SetBlockInsert installs a hook that runs after a request is accepted
// and before the SQLite commit. Tests use it to hold one request in
// flight. The hook is not set in production.
func (s *Server) SetBlockInsert(fn func(context.Context) error) {
	s.blockInsert = fn
}
