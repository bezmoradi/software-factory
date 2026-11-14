package remote

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/push"
	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/storage/memory"
)

const (
	cloneDirMode  = 0o775 | os.ModeSetgid
	cloneFileMode = 0o664
	maxBasePacks  = 2000
	trailerSize   = 20
)

var (
	packName                       = regexp.MustCompile(`^pack-([0-9a-f]{40})\.pack$`)
	_        session.SessionClones = Remote{}
)

func (r Remote) SessionClone(ctx context.Context, q session.CloneSpec) error {
	switch {
	case !session.ValidBox(q.Box), !repoName.MatchString(q.Repo), strings.Contains(q.Repo, ".."):
		return errors.New("the session's clone isn't one the engine makes")
	case !sha1Pattern.MatchString(q.Head):
		return errors.New("the session's start commit isn't a full commit id")
	case q.Branch != "" && !push.ValidBranch(q.Branch):
		return errors.New("the session's branch isn't one the engine writes")
	case r.OpenBox == nil:
		return errors.New("the engine can't open the session's folder")
	}
	base, done, err := r.baseGit(q.Repo)
	if err != nil {
		return err
	}
	defer done()
	box, err := r.OpenBox(r.Workspace, q.Box)
	if err != nil {
		return errors.New("the session's folder can't be opened: " + err.Error())
	}
	defer func() { _ = box.Close() }()
	repos, err := ownFolder(box, "repos")
	if err != nil {
		return err
	}
	defer func() { _ = repos.Close() }()
	if _, err := repos.Lstat(q.Repo); !errors.Is(err, fs.ErrNotExist) {
		return errors.New("the session's folder already holds a clone of " + q.Repo)
	}
	tmpName := ".clone-" + random()
	if err := repos.Mkdir(tmpName, 0o700); err != nil {
		return errors.New("a folder for the session's clone can't be made")
	}
	placed := false
	defer func() {
		if !placed {
			_ = repos.RemoveAll(tmpName)
		}
	}()
	tmp, err := repos.OpenRoot(tmpName)
	if err != nil {
		return errors.New("the session's clone folder can't be opened")
	}
	defer func() { _ = tmp.Close() }()
	if err := buildClone(ctx, tmp, base, q); err != nil {
		return err
	}
	if err := repos.Chmod(tmpName, cloneDirMode); err != nil {
		return errors.New("the session's clone can't be shared with its container")
	}
	if err := repos.Rename(tmpName, q.Repo); err != nil {
		return errors.New("the session's clone can't be put in place")
	}
	placed = true
	return nil
}

func ownFolder(parent *os.Root, name string) (*os.Root, error) {
	if err := parent.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, errors.New("the session's " + name + " folder can't be made")
	}
	seen, err := parent.Lstat(name)
	if err != nil || !seen.IsDir() || seen.Mode()&fs.ModeSymlink != 0 || !ownedByMe(seen) {
		return nil, errors.New("the session's " + name + " folder isn't the engine's own folder")
	}
	if seen.Mode().Perm() != 0o755 {
		if err := parent.Chmod(name, 0o755); err != nil {
			return nil, errors.New("the session's " + name + " folder can't be made the engine's alone")
		}
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, errors.New("the session's " + name + " folder can't be opened")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(seen, opened) {
		_ = root.Close()
		return nil, errors.New("the session's " + name + " folder changed while it was opened")
	}
	return root, nil
}

func buildClone(ctx context.Context, tmp, base *os.Root, q session.CloneSpec) error {
	for _, d := range []string{".git", ".git/objects", ".git/objects/info", ".git/objects/pack", ".git/refs", ".git/refs/heads", ".git/refs/tags"} {
		if err := tmp.Mkdir(d, 0o700); err != nil {
			return errors.New("the session clone's .git folder can't be made")
		}
	}
	git, err := tmp.OpenRoot(".git")
	if err != nil {
		return errors.New("the session clone's .git folder can't be opened")
	}
	defer func() { _ = git.Close() }()
	if err := sharePacks(ctx, base, git); err != nil {
		return err
	}
	if len(q.Objects) > 0 {
		if err := carryObjects(git, q.Objects, q.Head); err != nil {
			return err
		}
	}
	head := q.Head + "\n"
	files := []struct{ name, data string }{{"config", sessionConfig}}
	if q.MainBranch != "" && push.ValidBranch(q.MainBranch) && sha1Pattern.MatchString(q.Main) {
		files = append(files, struct{ name, data string }{"refs/heads/" + q.MainBranch, q.Main + "\n"})
		head = "ref: refs/heads/" + q.MainBranch + "\n"
	}
	if q.Branch != "" && q.Branch != q.MainBranch {
		files = append(files, struct{ name, data string }{"refs/heads/" + q.Branch, q.Head + "\n"})
	}
	files = append(files, struct{ name, data string }{"HEAD", head})
	for _, f := range files {
		if err := writeMode(git, f.name, []byte(f.data), 0o700, cloneFileMode); err != nil {
			return errors.New("the session clone's refs couldn't be written")
		}
	}
	return shareClone(tmp)
}

const sessionConfig = "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n\tsharedRepository = group\n"

func shareClone(tmp *os.Root) error {
	return fs.WalkDir(tmp.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case p == ".":
			return nil
		case d.IsDir():
			return tmp.Chmod(p, cloneDirMode)
		case d.Type().IsRegular() && strings.HasPrefix(p, ".git/objects/"):
			return nil
		case d.Type().IsRegular():
			return tmp.Chmod(p, cloneFileMode)
		}
		return nil
	})
}

func sharePacks(ctx context.Context, base, git *os.Root) error {
	src, err := base.Open("objects/pack")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("the engine's copy of the repository can't be read")
	}
	defer func() { _ = src.Close() }()
	entries, err := src.ReadDir(maxBasePacks + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return errors.New("the engine's copy of the repository can't be read")
	}
	if len(entries) > maxBasePacks {
		return errors.New("the engine's copy of the repository holds more packs than the engine shares")
	}
	dst, err := git.Open("objects/pack")
	if err != nil {
		return errors.New("the session clone's objects folder can't be opened")
	}
	defer func() { _ = dst.Close() }()
	for _, e := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m := packName.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		sum := m[1]
		idx := "pack-" + sum + ".idx"
		if !sealed(base, "objects/pack/"+e.Name(), sum, 0) || !sealed(base, "objects/pack/"+idx, sum, trailerSize) {
			continue
		}
		for _, name := range []string{e.Name(), idx} {
			if err := shareFile(base, git, src, dst, name); err != nil {
				return err
			}
		}
	}
	return nil
}

func sealed(root *os.Root, name, sum string, from int64) bool {
	f, err := root.OpenFile(name, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByMe(info) || info.Mode().Perm()&0o222 != 0 || info.Size() < from+trailerSize+12 {
		return false
	}
	got := make([]byte, trailerSize)
	if _, err := f.ReadAt(got, info.Size()-from-trailerSize); err != nil {
		return false
	}
	return hex.EncodeToString(got) == sum
}

func shareFile(base, git *os.Root, src, dst *os.File, name string) error {
	if err := linkAt(src, dst, name); err == nil {
		return nil
	}
	in, err := base.OpenFile("objects/pack/"+name, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return errors.New("a pack of the engine's copy can't be read")
	}
	defer func() { _ = in.Close() }()
	out, err := git.OpenFile("objects/pack/"+name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0o444)
	if err != nil {
		return errors.New("a pack couldn't be copied into the session's clone")
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = git.Remove("objects/pack/" + name)
		return errors.New("a pack couldn't be copied into the session's clone")
	}
	return nil
}

func carryObjects(git *os.Root, objects []push.Object, head string) error {
	sto := memory.NewStorage()
	hashes := make([]plumbing.Hash, 0, len(objects))
	for i := range objects {
		o := objects[i]
		objects[i].Data = nil
		t, err := plumbing.ParseObjectType(o.Kind)
		if err != nil || t == plumbing.OFSDeltaObject || t == plumbing.REFDeltaObject {
			return errors.New("the other session's clone gave an object of an unknown type")
		}
		obj := &plumbing.MemoryObject{}
		obj.SetType(t)
		if _, err := obj.Write(o.Data); err != nil || obj.Hash().String() != o.SHA {
			return errors.New("an object of the other session's clone doesn't match its name")
		}
		h, err := sto.SetEncodedObject(obj)
		if err != nil {
			return errors.New("the other session's commits couldn't be prepared")
		}
		hashes = append(hashes, h)
	}
	var buf bytes.Buffer
	if _, err := packfile.NewEncoder(&buf, sto, false).Encode(hashes, 0); err != nil {
		return errors.New("the other session's commits couldn't be packed")
	}
	if err := storePack(git, &buf, head); err != nil {
		return errors.New("the other session's commits couldn't be written into the session's clone: " + err.Error())
	}
	return nil
}
