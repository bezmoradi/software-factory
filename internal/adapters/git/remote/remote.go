package remote

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

const (
	tokenUser  = "x-access-token"
	MaxPack    = 4 << 30
	maxMessage = 400
	maxNames   = 100
)

var (
	sha1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repoName    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
)

type Remote struct {
	Workspace string
	CAFile    string
	Client    *http.Client
}

var _ session.Remote = Remote{}

func (r Remote) HTTPClient() (*http.Client, error) { return r.client() }

func (r Remote) client() (*http.Client, error) {
	if r.Client != nil {
		return r.Client, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if r.CAFile != "" {
		pem, err := os.ReadFile(r.CAFile)
		if err != nil {
			return nil, errors.New("the development CA file SF_DEV_GIT_CA names can't be read")
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("the development CA file SF_DEV_GIT_CA names holds no certificate")
		}
		tc.RootCAs = pool
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tc,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: time.Minute,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}}, nil
}

func endpoint(raw string) (*transport.Endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || u.Path == "/" {
		return nil, errors.New("the address isn't a plain https address")
	}
	ep, err := transport.NewEndpoint(u.String())
	if err != nil || ep.Protocol != "https" || ep.User != "" || ep.Password != "" {
		return nil, errors.New("the address isn't a plain https address")
	}
	return ep, nil
}

type conn struct {
	sess  transport.UploadPackSession
	adv   *packp.AdvRefs
	token string
	host  string
}

func (r Remote) open(ctx context.Context, raw, token string) (*conn, error) {
	ep, err := endpoint(raw)
	if err != nil {
		return nil, err
	}
	c, err := r.client()
	if err != nil {
		return nil, err
	}
	var auth transport.AuthMethod
	if token != "" {
		auth = &githttp.BasicAuth{Username: tokenUser, Password: token}
	}
	tr := githttp.NewClientWithOptions(c, &githttp.ClientOptions{RedirectPolicy: githttp.NoFollowRedirects})
	k := &conn{token: token, host: ep.Host}
	sess, err := tr.NewUploadPackSession(ep, auth)
	if err != nil {
		return nil, k.explain(ctx, err)
	}
	adv, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		_ = sess.Close()
		return nil, k.explain(ctx, err)
	}
	for _, f := range adv.Capabilities.Get(capability.ObjectFormat) {
		if f != "sha1" {
			_ = sess.Close()
			return nil, errors.New("the remote uses the " + clean(f, token) + " object format, and SHA-256 repositories aren't supported: the engine pushes with go-git v5, which handles SHA-1 object names only in this build")
		}
	}
	k.sess, k.adv = sess, adv
	return k, nil
}

func (k *conn) close() { _ = k.sess.Close() }

func (k *conn) mainBranch(fallback string) (string, string, error) {
	heads := 0
	for name := range k.adv.References {
		if strings.HasPrefix(name, "refs/heads/") {
			heads++
		}
	}
	if heads == 0 {
		return "", "", errors.New("the repository is empty; push a first commit to its main branch, then try again")
	}
	branch := ""
	for _, v := range k.adv.Capabilities.Get(capability.SymRef) {
		from, to, ok := strings.Cut(v, ":")
		if ok && from == "HEAD" {
			name, isBranch := strings.CutPrefix(to, "refs/heads/")
			if !isBranch || !push.ValidBranch(name) {
				return "", "", errors.New("the remote's main branch isn't a branch name the engine accepts")
			}
			branch = name
		}
	}
	if branch == "" {
		branch = fallback
	}
	if branch == "" {
		return "", "", errors.New("the remote doesn't say which branch is its main branch")
	}
	h, ok := k.adv.References["refs/heads/"+branch]
	if !ok {
		return "", "", fmt.Errorf("the remote has no branch %s", branch)
	}
	return branch, h.String(), nil
}

func (k *conn) explain(ctx context.Context, err error) error {
	var cert *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var dns *net.DNSError
	var op *net.OpError
	msg := ""
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired) && k.token == "":
		msg = "the git host wants a token for this repository; save one for its host on this page, then add it again"
	case errors.Is(err, transport.ErrAuthenticationRequired), errors.Is(err, transport.ErrAuthorizationFailed) && k.token != "":
		msg = "the git host refused the token saved for its host; check it can read this repository, or replace it"
	case errors.Is(err, transport.ErrAuthorizationFailed):
		msg = "the git host refused access to this repository"
	case errors.Is(err, transport.ErrRepositoryNotFound):
		msg = "the git host has no repository at that address, or the token can't see it"
	case errors.Is(err, transport.ErrEmptyRemoteRepository):
		msg = "the repository is empty; push a first commit to its main branch, then try again"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		msg = "the git host didn't answer in time"
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		msg = "the engine stopped before the git host answered"
	case errors.As(err, &cert), errors.As(err, &unknown), errors.As(err, &host):
		msg = "the git host's certificate isn't one this engine trusts"
	case errors.As(err, &dns):
		msg = "the git host's name, " + k.host + ", doesn't resolve"
	case errors.As(err, &op) && op.Op == "dial":
		msg = "the engine couldn't connect to " + k.host
	case strings.Contains(err.Error(), "http redirect"):
		msg = "the git host answered with a redirect, which the engine doesn't follow; give the address it redirects to"
	default:
		inner := err
		if ue, ok := errors.AsType[*plumbing.UnexpectedError](err); ok {
			inner = ue.Err
		} else if pe, ok := errors.AsType[*plumbing.PermanentError](err); ok {
			inner = pe.Err
		}
		msg = "the git host's answer couldn't be used: " + inner.Error()
	}
	return errors.New(clean(msg, k.token))
}

var userinfo = regexp.MustCompile(`(?i)(https?://)[^/@\s]*@`)

func clean(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte(tokenUser+":"+token)), "[token]")
		s = strings.ReplaceAll(s, token, "[token]")
	}
	s = userinfo.ReplaceAllString(s, "$1")
	if len(s) > maxMessage {
		s = s[:maxMessage]
	}
	return s
}

func random() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (k *conn) download(ctx context.Context, git *os.Root, want string, have []string) error {
	return k.downloadDepth(ctx, git, want, have, 0)
}

func (k *conn) downloadDepth(ctx context.Context, git *os.Root, want string, have []string, depth int) error {
	req := packp.NewUploadPackRequestFromCapabilities(k.adv.Capabilities)
	if depth > 0 && k.adv.Capabilities.Supports(capability.Shallow) {
		_ = req.Capabilities.Set(capability.Shallow)
		req.Depth = packp.DepthCommits(depth)
	}
	for _, c := range []capability.Capability{capability.ThinPack, capability.Sideband, capability.Sideband64k} {
		req.Capabilities.Delete(c)
	}
	if k.adv.Capabilities.Supports(capability.NoProgress) {
		_ = req.Capabilities.Set(capability.NoProgress)
	}
	req.Wants = []plumbing.Hash{plumbing.NewHash(want)}
	for _, h := range have {
		if sha1Pattern.MatchString(h) && h != want {
			req.Haves = append(req.Haves, plumbing.NewHash(h))
		}
	}
	res, err := k.sess.UploadPack(ctx, req)
	if errors.Is(err, transport.ErrEmptyUploadPackRequest) {
		return nil
	}
	if err != nil {
		return k.explain(ctx, err)
	}
	defer func() { _ = res.Close() }()
	if err := git.MkdirAll("objects/pack", 0o775); err != nil {
		return errors.New("the clone's objects/pack folder couldn't be made")
	}
	tmp := "objects/pack/tmp_pack_" + random()
	f, err := git.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return errors.New("the pack couldn't be written into the clone")
	}
	written := false
	defer func() {
		_ = f.Close()
		if !written {
			_ = git.Remove(tmp)
		}
	}()
	n, err := io.Copy(f, io.LimitReader(res, MaxPack+1))
	if err != nil {
		if ctx.Err() != nil {
			return k.explain(ctx, ctx.Err())
		}
		return errors.New(clean("the pack didn't arrive whole: "+err.Error(), k.token))
	}
	if n > MaxPack {
		return fmt.Errorf("the repository's pack is larger than %d GiB, more than the engine clones", MaxPack>>30)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return errors.New("the pack couldn't be read back")
	}
	w := new(idxfile.Writer)
	parser, err := packfile.NewParser(packfile.NewScanner(f), w)
	if err != nil {
		return errors.New("the remote sent a pack the engine can't read")
	}
	sum, err := parser.Parse()
	if err != nil {
		return errors.New("the remote sent a pack the engine can't read")
	}
	idx, err := w.Index()
	if err != nil {
		return errors.New("the pack's index couldn't be built")
	}
	if ok, err := idx.Contains(plumbing.NewHash(want)); err != nil || !ok {
		return errors.New("the remote's pack doesn't hold the commit it named")
	}
	base := "objects/pack/pack-" + sum.String()
	if _, err := git.Lstat(base + ".pack"); err == nil {
		return nil
	}
	tmpIdx := "objects/pack/tmp_idx_" + random()
	g, err := git.OpenFile(tmpIdx, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return errors.New("the pack's index couldn't be written into the clone")
	}
	_, err = idxfile.NewEncoder(g).Encode(idx)
	if cerr := g.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = git.Remove(tmpIdx)
		return errors.New("the pack's index couldn't be written into the clone")
	}
	_ = git.Chmod(tmp, 0o444)
	_ = git.Chmod(tmpIdx, 0o444)
	if err := git.Rename(tmp, base+".pack"); err != nil {
		_ = git.Remove(tmpIdx)
		return errors.New("the pack couldn't be put in place in the clone")
	}
	written = true
	if err := git.Rename(tmpIdx, base+".idx"); err != nil {
		_ = git.Remove(tmpIdx)
		return errors.New("the pack's index couldn't be put in place in the clone")
	}
	return nil
}

func writeFile(root *os.Root, name string, data []byte) error {
	dir := path.Dir(name)
	if err := root.MkdirAll(dir, 0o775); err != nil {
		return err
	}
	tmp := path.Join(dir, ".sf-"+random())
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		_ = root.Remove(tmp)
	}
	return err
}

func config(u string) string {
	return "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n\tsharedRepository = group\n" +
		"[remote \"origin\"]\n\turl = " + u + "\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n"
}

func (r Remote) Clone(ctx context.Context, q session.CloneRequest) (session.Cloned, error) {
	if !repoName.MatchString(q.Name) || strings.Contains(q.Name, "..") {
		return session.Cloned{}, errors.New("the folder name isn't one the engine uses")
	}
	if _, err := endpoint(q.URL); err != nil {
		return session.Cloned{}, err
	}
	ws, err := os.OpenRoot(r.Workspace)
	if err != nil {
		return session.Cloned{}, errors.New("the workspace can't be opened")
	}
	defer func() { _ = ws.Close() }()
	if err := ws.Mkdir("repos", 0o775); err != nil && !errors.Is(err, fs.ErrExist) {
		return session.Cloned{}, errors.New("the workspace's repos folder can't be made")
	}
	tmpName := "repos/.clone-" + random()
	if err := ws.Mkdir(tmpName, 0o700); err != nil {
		return session.Cloned{}, errors.New("a folder for the clone can't be made in the workspace")
	}
	placed := false
	defer func() {
		if !placed {
			_ = ws.RemoveAll(tmpName)
		}
	}()
	tmp, err := ws.OpenRoot(tmpName)
	if err != nil {
		return session.Cloned{}, errors.New("the clone's folder can't be opened")
	}
	defer func() { _ = tmp.Close() }()
	if info, err := tmp.Stat("."); err != nil || !info.IsDir() || !ownedByMe(info) || info.Mode().Perm()&0o077 != 0 {
		return session.Cloned{}, errors.New("the clone's folder was changed under the engine")
	}

	k, err := r.open(ctx, q.URL, q.Token)
	if err != nil {
		return session.Cloned{}, err
	}
	defer k.close()
	branch, head, err := k.mainBranch("")
	if err != nil {
		return session.Cloned{}, err
	}
	for _, d := range []string{".git/objects/info", ".git/objects/pack", ".git/refs/heads", ".git/refs/tags", ".git/refs/remotes/origin"} {
		if err := tmp.MkdirAll(d, 0o775); err != nil {
			return session.Cloned{}, errors.New("the clone's .git folder can't be made")
		}
	}
	git, err := tmp.OpenRoot(".git")
	if err != nil {
		return session.Cloned{}, errors.New("the clone's .git folder can't be opened")
	}
	defer func() { _ = git.Close() }()
	if err := k.download(ctx, git, head, nil); err != nil {
		return session.Cloned{}, err
	}
	files := []struct{ name, data string }{
		{"refs/heads/" + branch, head + "\n"},
		{"refs/remotes/origin/" + branch, head + "\n"},
		{"refs/remotes/origin/HEAD", "ref: refs/remotes/origin/" + branch + "\n"},
		{"config", config(q.URL)},
		{"HEAD", "ref: refs/heads/" + branch + "\n"},
	}
	for _, f := range files {
		if err := writeFile(git, f.name, []byte(f.data)); err != nil {
			return session.Cloned{}, errors.New("the clone's refs couldn't be written")
		}
	}
	if err := share(tmp); err != nil {
		return session.Cloned{}, errors.New("the clone couldn't be made group-writable for agent-host")
	}
	name, err := place(ws, tmpName, q.Name, q.Taken)
	if err != nil {
		return session.Cloned{}, err
	}
	placed = true
	return session.Cloned{Name: name, Branch: branch, Commit: head}, nil
}

func share(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return root.Chmod(p, 0o775|os.ModeSetgid)
		case d.Type().IsRegular() && strings.HasPrefix(p, ".git/objects/"):
			return root.Chmod(p, 0o444)
		case d.Type().IsRegular():
			return root.Chmod(p, 0o664)
		}
		return nil
	})
}

func place(ws *os.Root, tmpName, base string, taken func(string) bool) (string, error) {
	for i := 1; i <= maxNames; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		if taken != nil && taken(name) {
			continue
		}
		dest := "repos/" + name
		if _, err := ws.Lstat(dest); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := ws.Rename(tmpName, dest); err == nil {
			return name, nil
		}
	}
	return "", errors.New("no free folder name was found for the clone")
}

func (r Remote) Fetch(ctx context.Context, q session.FetchRequest) (session.Fetched, error) {
	if !repoName.MatchString(q.Name) || strings.Contains(q.Name, "..") {
		return session.Fetched{}, errors.New("the repository's folder name isn't one the engine uses")
	}
	if _, err := endpoint(q.URL); err != nil {
		return session.Fetched{}, err
	}
	ws, err := os.OpenRoot(r.Workspace)
	if err != nil {
		return session.Fetched{}, errors.New("the workspace can't be opened")
	}
	defer func() { _ = ws.Close() }()
	git, err := ws.OpenRoot(path.Join("repos", q.Name, ".git"))
	if err != nil {
		return session.Fetched{}, errors.New("the clone's .git folder is missing from the workspace")
	}
	defer func() { _ = git.Close() }()
	k, err := r.open(ctx, q.URL, q.Token)
	if err != nil {
		return session.Fetched{}, err
	}
	defer k.close()
	branch, head, err := k.mainBranch(q.Branch)
	if err != nil {
		return session.Fetched{}, err
	}
	if !slices.Contains(q.Have, head) {
		if err := k.download(ctx, git, head, q.Have); err != nil {
			return session.Fetched{}, err
		}
	}
	if err := writeFile(git, "refs/remotes/origin/"+branch, []byte(head+"\n")); err != nil {
		return session.Fetched{}, errors.New("refs/remotes/origin/" + branch + " couldn't be updated in the clone")
	}
	return session.Fetched{Branch: branch, Commit: head}, nil
}

func (r Remote) FetchCommit(ctx context.Context, raw, commit, dir string) error {
	if !sha1Pattern.MatchString(commit) {
		return errors.New("the pinned commit isn't a full SHA-1 hash")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.New("the folder for the download can't be opened")
	}
	defer func() { _ = root.Close() }()
	if names, err := fs.ReadDir(root.FS(), "."); err != nil || len(names) > 0 {
		return errors.New("the folder for the download isn't empty")
	}
	k, err := r.open(ctx, raw, "")
	if err != nil {
		return err
	}
	defer k.close()
	if !k.offers(commit) {
		return errors.New("the remote neither lists the pinned commit nor allows asking for it by its hash")
	}
	if err := root.MkdirAll("objects/pack", 0o755); err != nil {
		return errors.New("the download's objects folder can't be made")
	}
	if err := k.downloadDepth(ctx, root, commit, nil, 1); err != nil {
		return err
	}
	if err := writeFile(root, "HEAD", []byte(commit+"\n")); err != nil {
		return errors.New("the download's HEAD can't be written")
	}
	return nil
}

func (k *conn) offers(commit string) bool {
	h := plumbing.NewHash(commit)
	for _, v := range k.adv.References {
		if v == h {
			return true
		}
	}
	for _, v := range k.adv.Peeled {
		if v == h {
			return true
		}
	}
	return k.adv.Capabilities.Supports(capability.AllowReachableSHA1InWant)
}
