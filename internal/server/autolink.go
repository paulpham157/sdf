package server

import (
	"io"

	"github.com/paulpham157/sdf/internal/decision"
	"github.com/paulpham157/sdf/internal/record"
)

// insertRecords stores records and, for each sdf.git.commit event, appends
// any auto-link implements edges before returning. The HTTP and gRPC
// ingest paths both use this, so a 200 or a successful Export is not
// acknowledged until those edges have been committed. Decision ids on the
// hook event are read for linking and are not stored on the row.
func (s *Server) insertRecords(records []record.Record) error {
	stored := decision.WithoutGitDecisionAttributes(records)
	if _, err := s.Store.Insert(stored); err != nil {
		return err
	}
	log := s.log
	if log == nil {
		log = io.Discard
	}
	return decision.AutoLinkGitCommits(s.Store, records, log)
}
