package boxes

import (
	"context"
	"net/http"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

type verifierJSON struct {
	Run          string `json:"run"`
	State        string `json:"state"`
	ExitCode     int    `json:"exitCode"`
	OOMKilled    bool   `json:"oomKilled"`
	Error        string `json:"error"`
	StartedAt    int64  `json:"startedAt"`
	FinishedAt   int64  `json:"finishedAt"`
	Image        string `json:"image"`
	Digest       string `json:"digest"`
	LimitSeconds int64  `json:"limitSeconds"`
	Recipe       string `json:"recipe"`
}

type verifierAnswerJSON struct {
	Version  string        `json:"version"`
	Verifier *verifierJSON `json:"verifier"`
}

type verifierLogJSON struct {
	Version string `json:"version"`
	Log     string `json:"log"`
}

type verifiersJSON struct {
	Version   string    `json:"version"`
	Verifiers []boxJSON `json:"verifiers"`
}

func millisOf(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func (s *server) verifiers(base context.Context, w http.ResponseWriter, r *http.Request, rt routed) {
	if s.cfg.Verifiers == nil {
		s.refuse(w, r, refuse(http.StatusNotFound, ReasonRoute, "the box helper has no such route"))
		return
	}
	if rt.name != routeVerifiers {
		digest := rt.snapshot
		if rt.name == routeVerify && digest == "" {
			digest = "-"
		}
		if err := badVerifier(rt.id, digest); err != nil {
			s.failed(w, r, err)
			return
		}
	}
	limit := otherFor
	if r.Method == http.MethodPut {
		limit = startFor
	}
	ctx, cancel := context.WithTimeout(base, limit)
	defer cancel()
	switch {
	case rt.name == routeVerifiers:
		list, err := s.cfg.Verifiers.ListVerifiers(ctx)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		out := verifiersJSON{Version: WireVersion, Verifiers: []boxJSON{}}
		for _, b := range list {
			out.Verifiers = append(out.Verifiers, boxOf(b))
		}
		writeJSON(w, http.StatusOK, out)
	case rt.name == routeVerify:
		info, err := s.cfg.Verifiers.StartVerifier(ctx, rt.id, rt.snapshot)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, verifierAnswerJSON{Version: WireVersion, Verifier: &verifierJSON{Run: info.Run, State: "started", Image: info.Image, Digest: info.Digest,
			LimitSeconds: int64(info.Limit / time.Second), Recipe: info.Recipe}})
	case rt.name == routeVerifyLog:
		text, err := s.cfg.Verifiers.VerifierLog(ctx, rt.id)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, verifierLogJSON{Version: WireVersion, Log: text})
	case r.Method == http.MethodDelete:
		if err := s.cfg.Verifiers.RemoveVerifier(ctx, rt.id); err != nil {
			s.failed(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		st, err := s.cfg.Verifiers.VerifierState(ctx, rt.id)
		if err != nil {
			s.failed(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, verifierAnswerJSON{Version: WireVersion, Verifier: stateJSONOf(st)})
	}
}

func stateJSONOf(st session.VerifierState) *verifierJSON {
	return &verifierJSON{Run: st.Run, State: st.State, ExitCode: st.ExitCode, OOMKilled: st.OOMKilled, Error: st.Error, StartedAt: millisOf(st.StartedAt),
		FinishedAt: millisOf(st.FinishedAt), Image: st.Image, Digest: st.Digest, LimitSeconds: int64(st.Limit / time.Second)}
}
