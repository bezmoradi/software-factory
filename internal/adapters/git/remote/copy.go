package remote

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	copyBlock     = 4 << 10
	copyDirMode   = 0o755
	copyFileMode  = 0o444
	copyTempMode  = 0o700
	copyTempStart = ".fetch-"
)

var errRedirect = errors.New("the git host answered with a redirect, which the engine doesn't follow for a copy")

type CopyTrees interface {
	WriteTreeBounded(path, commit, dest string, limit, block int64, entries int) error
}

type Copies struct {
	Workspace string
	CAFile    string
	Trees     CopyTrees
	Own       func() []netip.Prefix
	Private   []string
}

var _ session.CopyFiles = Copies{}

func (c Copies) own() []netip.Prefix {
	if c.Own == nil {
		return nil
	}
	return c.Own()
}

type budget struct {
	left atomic.Int64
	over atomic.Bool
}

type budgetBody struct {
	rc io.ReadCloser
	b  *budget
}

func (b *budgetBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if b.b.left.Add(-int64(n)) < 0 {
		b.b.over.Store(true)
		return n, errPackOver
	}
	return n, err
}

func (b *budgetBody) Close() error { return b.rc.Close() }

type budgetTransport struct {
	base http.RoundTripper
	b    *budget
}

func (t budgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.b.over.Load() {
		return nil, errPackOver
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &budgetBody{rc: resp.Body, b: t.b}
	return resp, nil
}

type refusedDial struct {
	mu  sync.Mutex
	why string
}

func (r *refusedDial) set(why string) {
	r.mu.Lock()
	if r.why == "" {
		r.why = why
	}
	r.mu.Unlock()
}

func (r *refusedDial) get() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.why
}

func (c Copies) client(b *budget, refused *refusedDial) (*http.Client, *http.Transport, error) {
	tc, err := tlsConfig(c.CAFile)
	if err != nil {
		return nil, nil, err
	}
	own := c.own()
	checked := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			refused.set("the engine couldn't tell which address it was connecting to")
			return errors.New("unreadable address")
		}
		if why := session.CopyAddressRefused(ap.Addr(), own); why != "" {
			refused.set("the host resolved to " + ap.Addr().Unmap().String() + ", " + why + ", and the engine fetches copies only from public hosts")
			return errors.New("refused address")
		}
		return nil
	}}
	plain := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err == nil && slices.Contains(c.Private, host) {
			return plain.DialContext(ctx, network, addr)
		}
		return checked.DialContext(ctx, network, addr)
	}
	base := &http.Transport{
		Proxy:                 nil,
		DialContext:           dial,
		TLSClientConfig:       tc,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: time.Minute,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{
		Transport:     budgetTransport{base: base, b: b},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
	}, base, nil
}

func plainCopyFolder(parent *os.Root, name string) error {
	if err := parent.Mkdir(name, copyDirMode); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := parent.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 || !ownedByMe(info) {
		return errors.New("a folder of the copies isn't the engine's own")
	}
	return parent.Chmod(name, copyDirMode)
}

func (c Copies) root() (*os.Root, error) {
	if !path.IsAbs(c.Workspace) || path.Clean(c.Workspace) != c.Workspace {
		return nil, errors.New("the workspace can't be opened")
	}
	ws, err := os.OpenRoot(c.Workspace)
	if err != nil {
		return nil, errors.New("the workspace can't be opened")
	}
	defer func() { _ = ws.Close() }()
	if err := plainCopyFolder(ws, session.CopiesFolder); err != nil {
		return nil, errors.New("the workspace's folder of copies isn't the engine's own folder")
	}
	return ws.OpenRoot(session.CopiesFolder)
}

func (c Copies) Sweep() {
	r, err := c.root()
	if err != nil {
		return
	}
	defer func() { _ = r.Close() }()
	f, err := r.Open(".")
	if err != nil {
		return
	}
	entries, _ := f.ReadDir(-1)
	_ = f.Close()
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), copyTempStart) {
			_ = r.RemoveAll(e.Name())
		}
	}
}

func (c Copies) MakeBox(box string) error {
	if !session.ValidBox(box) {
		return errors.New("copies: bad place")
	}
	r, err := c.root()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return plainCopyFolder(r, box)
}

func (c Copies) RemoveBox(box string) error {
	if !session.ValidBox(box) {
		return errors.New("copies: bad place")
	}
	r, err := c.root()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if _, err := r.Lstat(box); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return r.RemoveAll(box)
}

func copyPlace(box, folder string) (string, bool) {
	if !session.ValidBox(box) || folder == "" || path.Clean(folder) != folder || strings.HasPrefix(folder, "/") || strings.ContainsRune(folder, 0) {
		return "", false
	}
	for part := range strings.SplitSeq(folder, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
			return "", false
		}
	}
	return box + "/" + folder, true
}

func (c Copies) Remove(box, folder string) error {
	at, ok := copyPlace(box, folder)
	if !ok {
		return errors.New("copies: bad place")
	}
	r, err := c.root()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	if _, err := r.Lstat(at); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return r.RemoveAll(at)
}

func (c Copies) Fetch(ctx context.Context, q session.CopyFetch) (string, error) {
	u, folder, why := session.CopyAddress(q.URL, q.Ref)
	if why != "" || u != q.URL || folder != q.Folder {
		return "", errors.New("the copy's address or folder isn't one the engine made")
	}
	at, ok := copyPlace(q.Box, q.Folder)
	if !ok || c.Trees == nil {
		return "", errors.New("the copy's place isn't one the engine uses")
	}
	r, err := c.root()
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	if err := plainCopyFolder(r, q.Box); err != nil {
		return "", errors.New("the session's folder of copies isn't the engine's own")
	}
	tmp := copyTempStart + random()
	if err := r.Mkdir(tmp, copyTempMode); err != nil {
		return "", errors.New("a folder for the fetch can't be made in the workspace")
	}
	defer func() { _ = r.RemoveAll(tmp) }()
	for _, d := range []string{tmp + "/git", tmp + "/git/objects", tmp + "/git/objects/pack", tmp + "/tree"} {
		if err := r.Mkdir(d, copyTempMode); err != nil {
			return "", errors.New("a folder for the fetch can't be made in the workspace")
		}
	}
	commit, err := c.download(ctx, r, tmp+"/git", q)
	if err != nil {
		return "", err
	}
	gitPath := path.Join(c.Workspace, session.CopiesFolder, tmp, "git")
	treePath := path.Join(c.Workspace, session.CopiesFolder, tmp, "tree")
	if err := c.Trees.WriteTreeBounded(gitPath, commit, treePath, session.MaxCopyFiles, copyBlock, session.MaxCopyEntries); err != nil {
		if errors.Is(err, push.ErrTooLarge) {
			return "", &session.CopyLimitError{Limit: session.CopyLimitFiles}
		}
		return "", errors.New("the repository can't be copied safely: " + clean(err.Error(), q.Token))
	}
	tree, err := r.OpenRoot(tmp + "/tree")
	if err != nil {
		return "", errors.New("the copy's files can't be opened")
	}
	err = readOnly(tree)
	_ = tree.Close()
	if err != nil {
		return "", errors.New("the copy's files couldn't be made read-only")
	}
	if err := r.Chmod(tmp+"/tree", copyDirMode); err != nil {
		return "", errors.New("the copy's files couldn't be made read-only")
	}
	parent := path.Dir(at)
	for d := range ancestors(parent) {
		if err := plainCopyFolder(r, d); err != nil {
			return "", errors.New("the copy's folder can't be made")
		}
	}
	if _, err := r.Lstat(at); err == nil {
		if err := r.RemoveAll(at); err != nil {
			return "", errors.New("an earlier copy in the same place can't be removed")
		}
	}
	if err := r.Rename(tmp+"/tree", at); err != nil {
		return "", errors.New("the copy can't be put in place")
	}
	return commit, nil
}

func ancestors(p string) func(func(string) bool) {
	return func(yield func(string) bool) {
		parts := strings.Split(p, "/")
		for i := range parts {
			if !yield(strings.Join(parts[:i+1], "/")) {
				return
			}
		}
	}
}

func readOnly(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			return nil
		case d.IsDir():
			return root.Chmod(p, copyDirMode)
		case d.Type().IsRegular():
			return root.Chmod(p, copyFileMode)
		}
		return errors.New("a special file")
	})
}

func (c Copies) download(ctx context.Context, r *os.Root, gitDir string, q session.CopyFetch) (string, error) {
	b := &budget{}
	b.left.Store(session.MaxCopyBytes)
	refused := &refusedDial{}
	hc, tr, err := c.client(b, refused)
	if err != nil {
		return "", err
	}
	defer tr.CloseIdleConnections()
	fail := func(err error) error {
		switch {
		case b.over.Load() || errors.Is(err, errPackOver):
			return &session.CopyLimitError{Limit: session.CopyLimitBytes}
		case refused.get() != "":
			return errors.New(refused.get())
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return &session.CopyLimitError{Limit: session.CopyLimitTime}
		}
		return err
	}
	k, err := openWith(ctx, hc, q.URL, q.Token, "ask for the copy again")
	if err != nil {
		return "", fail(err)
	}
	defer k.close()
	want, err := k.wanted(q.Ref)
	if err != nil {
		return "", err
	}
	git, err := r.OpenRoot(gitDir)
	if err != nil {
		return "", errors.New("the fetch's folder can't be opened")
	}
	defer func() { _ = git.Close() }()
	if err := k.downloadUpTo(ctx, git, want, nil, 1, nil, session.MaxCopyBytes); err != nil {
		return "", fail(err)
	}
	if err := writeFile(git, "HEAD", []byte(want+"\n")); err != nil {
		return "", errors.New("the fetch's HEAD can't be written")
	}
	return want, nil
}

func (k *conn) wanted(ref string) (string, error) {
	if ref == "" {
		_, head, err := k.mainBranch("")
		return head, err
	}
	if h, ok := k.adv.References["refs/heads/"+ref]; ok {
		return h.String(), nil
	}
	if h, ok := k.adv.Peeled["refs/tags/"+ref]; ok {
		return h.String(), nil
	}
	if h, ok := k.adv.References["refs/tags/"+ref]; ok {
		return h.String(), nil
	}
	return "", errors.New("the repository has no branch or tag " + ref)
}
