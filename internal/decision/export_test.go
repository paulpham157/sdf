package decision

// SetBeforeAppendEdgeForTest runs f in LinkWithWarnings just before the edge's
// transaction, and returns a func that clears it.
func SetBeforeAppendEdgeForTest(f func()) func() {
	beforeAppendEdge = f
	return func() { beforeAppendEdge = nil }
}
