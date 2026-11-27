package server

import "context"

// SetBlockInsert installs a hook that runs after a request is accepted
// and before the SQLite commit. Tests use it to hold one request in
// flight. The hook is not set in production.
func (s *Server) SetBlockInsert(fn func(context.Context) error) {
	s.blockInsert = fn
}

// SetBlockAfterInsert delays the success response after the SQLite
// commit. The row is already stored. Shutdown must still let the
// handler write 200.
func (s *Server) SetBlockAfterInsert(fn func(context.Context)) {
	s.blockAfterInsert = fn
}

// BudgetUsed is the in-flight byte reservation. Tests use it to prove a
// stalled gRPC read still holds its reservation.
func (s *Server) BudgetUsed() int64 {
	if s == nil || s.budget == nil {
		return 0
	}
	return s.budget.Used()
}

// AdmitRPC runs the gRPC tap on ctx. Tests use it to admit a call whose
// context is already cancelled.
func (s *Server) AdmitRPC(ctx context.Context) (context.Context, error) {
	return s.admitRPC(ctx, nil)
}

// ReadingRPCs is how many admitted gRPC calls are still tracked. The tap
// creates that state only after auth succeeds. Tests use the count to
// prove an unauthenticated client does not accumulate per-stream state.
func (s *Server) ReadingRPCs() int {
	if s == nil {
		return 0
	}
	s.phaseMu.Lock()
	defer s.phaseMu.Unlock()
	return len(s.reads)
}
