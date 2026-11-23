package ui

import (
	"io"
	"net/http"
)

// WriteJSONForTest runs the handler's JSON writer with log as its log.
func WriteJSONForTest(log io.Writer, w http.ResponseWriter, r *http.Request, encode func(io.Writer) error) {
	(&handler{log: log}).writeJSON(w, r, encode)
}
