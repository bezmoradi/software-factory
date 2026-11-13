//go:build !unix

package agenthost

import (
	"context"
	"net/http"
)

func (s *Server) answerTrust(context.Context, commandJSON, bool) (bool, *problem) {
	return false, fail(http.StatusNotImplemented, "trust", "Trusting a folder from the page needs a Unix agent container.")
}
