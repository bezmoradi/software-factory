package push

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

const (
	tokenUser = "x-access-token"
	maxReason = 500
)

type Pusher struct {
	Reader push.GitReader
	Client *http.Client
}

var _ push.Pusher = Pusher{}

func defaultClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 10 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}}
}

func (p Pusher) transport() transport.Transport {
	c := p.Client
	if c == nil {
		c = defaultClient()
	}
	return githttp.NewClientWithOptions(c, &githttp.ClientOptions{RedirectPolicy: githttp.NoFollowRedirects})
}

var sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func endpoint(t push.Target) (*transport.Endpoint, error) {
	if t.Token == "" {
		return nil, errors.New("no token was given for the push")
	}
	if !push.ValidBranch(t.Branch) {
		return nil, errors.New("the branch name isn't one the engine pushes to")
	}
	u, err := url.Parse(t.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || u.Path == "/" {
		return nil, errors.New("the remote URL isn't a plain https address")
	}
	ep, err := transport.NewEndpoint(u.String())
	if err != nil || ep.Protocol != "https" || ep.User != "" || ep.Password != "" {
		return nil, errors.New("the remote URL isn't a plain https address")
	}
	return ep, nil
}

func auth(t push.Target) *githttp.BasicAuth {
	return &githttp.BasicAuth{Username: tokenUser, Password: t.Token}
}

func clean(err error, t push.Target) string {
	if err == nil {
		return ""
	}
	return cleanText(err.Error(), t)
}

var userinfo = regexp.MustCompile(`(?i)(https?://)[^/@\s]*@`)

func cleanText(s string, t push.Target) string {
	if t.Token != "" {
		s = strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte(tokenUser+":"+t.Token)), "[token]")
		s = strings.ReplaceAll(s, t.Token, "[token]")
	}
	s = userinfo.ReplaceAllString(s, "$1")
	if len(s) > maxReason {
		s = s[:maxReason]
	}
	return s
}

func (p Pusher) Remote(ctx context.Context, t push.Target) (push.RemoteState, error) {
	ep, err := endpoint(t)
	if err != nil {
		return push.RemoteState{}, err
	}
	sess, err := p.transport().NewUploadPackSession(ep, auth(t))
	if err != nil {
		return push.RemoteState{}, errors.New(clean(err, t))
	}
	defer func() { _ = sess.Close() }()
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return push.RemoteState{Empty: true}, nil
	}
	if err != nil {
		return push.RemoteState{}, fmt.Errorf("the remote couldn't be asked for its branches: %s", clean(err, t))
	}
	return remoteState(adv, t.Branch)
}

func remoteState(adv *packp.AdvRefs, branch string) (push.RemoteState, error) {
	var s push.RemoteState
	heads := 0
	for name := range adv.References {
		if strings.HasPrefix(name, "refs/heads/") {
			heads++
		}
	}
	if h, ok := adv.References["refs/heads/"+branch]; ok {
		s.BranchCommit = h.String()
	}
	if heads == 0 {
		s.Empty = true
		return s, nil
	}
	for _, v := range adv.Capabilities.Get(capability.SymRef) {
		from, to, ok := strings.Cut(v, ":")
		if ok && from == "HEAD" {
			name, isBranch := strings.CutPrefix(to, "refs/heads/")
			if !isBranch || !push.ValidBranch(name) {
				return push.RemoteState{}, errors.New("the remote's main branch isn't a branch name the engine accepts")
			}
			s.Main = name
		}
	}
	if s.Main == "" {
		return push.RemoteState{}, errors.New("the remote doesn't say which branch is its main branch")
	}
	h, ok := adv.References["refs/heads/"+s.Main]
	if !ok {
		return push.RemoteState{}, errors.New("the remote names a main branch it doesn't list")
	}
	s.MainCommit = h.String()
	return s, nil
}

func failed(reason string, previous string) push.Result {
	return push.Result{Outcome: push.Failed, Reason: reason, Previous: previous}
}

func unclear(reason string, previous string) push.Result {
	return push.Result{Outcome: push.Unclear, Reason: reason, Previous: previous}
}

func rejected(reason string, previous string) push.Result {
	return push.Result{Outcome: push.Rejected, Reason: reason, Previous: previous}
}

func (p Pusher) Push(ctx context.Context, req push.Request) push.Result {
	t := req.Target
	ep, err := endpoint(t)
	if err != nil {
		return failed(err.Error(), "")
	}
	if !sha1Pattern.MatchString(req.Commit) {
		return failed("the commit isn't a full commit id", "")
	}
	if req.Expected != "" && !sha1Pattern.MatchString(req.Expected) {
		return failed("the expected remote commit isn't a full commit id", "")
	}
	if p.Reader == nil {
		return failed("the pusher has no repository reader", "")
	}
	sess, err := p.transport().NewReceivePackSession(ep, auth(t))
	if err != nil {
		return failed(clean(err, t), "")
	}
	defer func() { _ = sess.Close() }()
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		return failed("the remote couldn't be asked for its branches: "+clean(err, t), "")
	}
	ref := plumbing.ReferenceName("refs/heads/" + t.Branch)
	current := ""
	if h, ok := adv.References[ref.String()]; ok {
		current = h.String()
	}
	if current != req.Expected {
		return rejected(fmt.Sprintf("the remote branch is at %s, not at %s, which the gate checked", orNone(current), orNone(req.Expected)), current)
	}
	if current == req.Commit {
		return push.Result{Outcome: push.Pushed, Reason: "the remote branch already has this commit", Previous: current}
	}
	if current != "" && !req.Force {
		ok, err := p.Reader.IsAncestor(req.Repo, current, req.Commit)
		if err != nil || !ok {
			return rejected("the commit doesn't build on the remote branch, so the push wouldn't be a fast-forward", current)
		}
	}
	if !adv.Capabilities.Supports(capability.ReportStatus) {
		return failed("the remote doesn't report what it did with a push", current)
	}
	var haves []string
	for _, h := range adv.References {
		haves = append(haves, h.String())
	}
	objects, err := p.Reader.ObjectsToSend(req.Repo, req.Commit, haves)
	if errors.Is(err, push.ErrTooLarge) {
		return failed(cleanText(strings.TrimPrefix(err.Error(), push.ErrTooLarge.Error()+": "), t), current)
	}
	if err != nil {
		return failed("the commit's objects couldn't be read: "+cleanText(err.Error(), t), current)
	}
	pack, err := encode(objects)
	if err != nil {
		return failed(err.Error(), current)
	}
	update := packp.NewReferenceUpdateRequestFromCapabilities(adv.Capabilities)
	for _, c := range []capability.Capability{capability.Sideband64k, capability.Sideband} {
		if adv.Capabilities.Supports(c) {
			if err := update.Capabilities.Set(c); err != nil {
				return failed("the push couldn't be prepared", current)
			}
			break
		}
	}
	update.Commands = []*packp.Command{{Name: ref, Old: hashOf(current), New: plumbing.NewHash(req.Commit)}}
	update.Packfile = io.NopCloser(bytes.NewReader(pack))
	report, err := sess.ReceivePack(ctx, update)
	if report == nil {
		if err == nil {
			return unclear("the remote didn't report what it did with the push", current)
		}
		return unclear("the push was sent, but its answer was lost: "+clean(err, t), current)
	}
	if report.UnpackStatus != "ok" {
		return failed("the remote couldn't unpack the push: "+cleanText(report.UnpackStatus, t), current)
	}
	for _, s := range report.CommandStatuses {
		if s.ReferenceName != ref {
			continue
		}
		if s.Status != "ok" {
			return rejected("the remote refused the push: "+cleanText(s.Status, t), current)
		}
		return push.Result{Outcome: push.Pushed, Previous: current}
	}
	return failed("the remote didn't report on the branch", current)
}

func orNone(sha string) string {
	if sha == "" {
		return "nothing"
	}
	return sha
}

func hashOf(sha string) plumbing.Hash {
	if sha == "" {
		return plumbing.ZeroHash
	}
	return plumbing.NewHash(sha)
}

func encode(objects []push.Object) ([]byte, error) {
	sto := memory.NewStorage()
	hashes := make([]plumbing.Hash, 0, len(objects))
	for i := range objects {
		o := objects[i]
		objects[i].Data = nil
		t, err := plumbing.ParseObjectType(o.Kind)
		if err != nil || t == plumbing.OFSDeltaObject || t == plumbing.REFDeltaObject {
			return nil, errors.New("the reader gave an object of an unknown type")
		}
		obj := &plumbing.MemoryObject{}
		obj.SetType(t)
		if _, err := obj.Write(o.Data); err != nil {
			return nil, errors.New("an object couldn't be prepared")
		}
		if obj.Hash().String() != o.SHA {
			return nil, errors.New("an object's content doesn't match its name")
		}
		h, err := sto.SetEncodedObject(obj)
		if err != nil {
			return nil, errors.New("an object couldn't be prepared")
		}
		hashes = append(hashes, h)
	}
	var buf bytes.Buffer
	if _, err := packfile.NewEncoder(&buf, sto, false).Encode(hashes, 0); err != nil {
		return nil, errors.New("the pack couldn't be built")
	}
	return buf.Bytes(), nil
}
