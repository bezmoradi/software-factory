package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	binDir    = ".sf-dev/bin"
	binName   = "software-factory"
	image     = "software-factory:local"
	pollEvery = 500 * time.Millisecond
	settle    = 300 * time.Millisecond
)

var (
	compose  = []string{"compose", "-f", "compose.yaml", "-f", "compose.live.yaml"}
	roots    = []string{"cmd", "configs", "internal", "tools", "web", "go.mod", "go.sum", "README.md", "compose.yaml", "plugin", "Dockerfile"}
	skipDirs = map[string]bool{"node_modules": true, "dist": true, ".git": true}
	watched  = map[string]bool{".go": true, ".ts": true, ".css": true, ".html": true, ".svg": true, ".json": true, ".md": true, ".mod": true, ".sum": true, ".yaml": true}
)

type stamp struct {
	mod  time.Time
	size int64
}

type change struct {
	page  bool
	code  bool
	image bool
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); !errors.Is(err, context.Canceled) {
		say("%v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	if _, err := os.Stat("compose.live.yaml"); err != nil {
		return errors.New("run make dev from the repository's root folder")
	}
	arch, err := dockerArch(ctx)
	if err != nil {
		return err
	}
	seen := scan()
	if err := build(ctx, arch, change{page: true, code: true}); err != nil {
		return err
	}
	if err := start(ctx, true, false); err != nil {
		return err
	}
	url := address(ctx)
	say("Software Factory is running the code in this folder: %s", url)
	say("Watching for changes. Ctrl-C stops watching and leaves the stack running; make up goes back to the image.")
	say("Stop make dev before a pull, a branch switch or a checkout of code you haven't read: whatever is in this folder runs as the engine, which holds your git tokens.")
	say("The Dockerfile and plugin/ come from the %s image as the last make up built it.", image)
	openPage(url)
	var owed change
	head := checkedOut()
	for {
		next, c, err := wait(ctx, seen, &head)
		if err != nil {
			return err
		}
		seen = next
		if c.image {
			say("The Dockerfile and plugin/ are built into the image, which make dev doesn't rebuild: press Ctrl-C, run make up, then make dev again.")
		}
		if !c.page && !c.code {
			continue
		}
		owed.page = owed.page || c.page
		owed.code = owed.code || c.code
		say("Change seen, rebuilding…")
		if err := build(ctx, arch, owed); err != nil {
			owed.page = true
			say("The build failed, so the running stack wasn't touched. Fix the error and save again; the next build makes the page again too.")
			continue
		}
		pageOnly := !owed.code
		if err := start(ctx, false, pageOnly); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			say("The stack didn't come back healthy: %v. Check docker compose logs engine, then save again.", err)
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		owed = change{}
		if pageOnly {
			say("Only the page changed, so only the engine was restarted. The page reloads itself once it reconnects.")
		} else {
			say("Restarted all four containers with the new code. The page reloads itself once it reconnects.")
		}
	}
}

func wait(ctx context.Context, seen map[string]stamp, head *string) (map[string]stamp, change, error) {
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, change{}, ctx.Err()
		case <-tick.C:
		}
		now := scan()
		c := diff(seen, now)
		h := checkedOut()
		if !c.page && !c.code && !c.image {
			*head = h
			continue
		}
		if h != *head {
			return nil, change{}, errors.New("the checked-out commit changed along with the files (a pull, a branch switch, a checkout or a rebase), so make dev stopped instead of running that code as the engine. The stack keeps running the last build. Read the change, then run make dev again")
		}
		for {
			select {
			case <-ctx.Done():
				return nil, change{}, ctx.Err()
			case <-time.After(settle):
			}
			later := scan()
			more := diff(now, later)
			if !more.page && !more.code && !more.image {
				return later, c, nil
			}
			c.page = c.page || more.page
			c.code = c.code || more.code
			c.image = c.image || more.image
			now = later
		}
	}
}

func scan() map[string]stamp {
	out := map[string]stamp{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != root && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".") && d.Name() != ".claude-plugin") {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !watched[filepath.Ext(p)] && !inImage(p) {
				return nil
			}
			if info, err := d.Info(); err == nil {
				out[p] = stamp{mod: info.ModTime(), size: info.Size()}
			}
			return nil
		})
	}
	return out
}

func diff(before, after map[string]stamp) change {
	var c change
	note := func(p string) {
		switch {
		case inImage(p):
			c.image = true
		case strings.HasPrefix(p, "web"+string(filepath.Separator)) || p == "README.md":
			c.page = true
		default:
			c.code = true
		}
	}
	for p, s := range after {
		if b, ok := before[p]; !ok || b != s {
			note(p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			note(p)
		}
	}
	return c
}

func inImage(p string) bool {
	return p == "Dockerfile" || strings.HasPrefix(p, "plugin"+string(filepath.Separator))
}

func build(ctx context.Context, arch string, c change) error {
	if c.page {
		if err := cmd(ctx, nil, "npm", "--prefix", "web", "run", "--silent", "build"); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(binDir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(binDir, binName+".new")
	env := []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + arch}
	if err := cmd(ctx, env, "go", "build", "-trimpath", "-o", tmp, "./cmd"); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(binDir, binName))
}

func start(ctx context.Context, first, engineOnly bool) error {
	args := append(append([]string{}, compose...), "up", "-d", "--wait", "--force-recreate")
	if engineOnly {
		args = append(args, "--no-deps", "engine")
	}
	if first && cmd(ctx, nil, "docker", "image", "inspect", "--format", "{{.Id}}", image) != nil {
		say("The %s image isn't built yet; building it once.", image)
		args = append(args, "--build")
	}
	c := exec.Command("docker", args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		say("Letting the restart finish before stopping, so no container is left half-recreated…")
		return <-done
	}
}

func address(ctx context.Context) string {
	args := append(append([]string{}, compose...), "port", "engine", "8080")
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if hostPort := strings.TrimSpace(string(out)); err == nil && hostPort != "" {
		return "http://" + hostPort
	}
	return "http://127.0.0.1:7799"
}

func checkedOut() string {
	head, err := os.ReadFile(filepath.Join(".git", "HEAD"))
	if err != nil {
		return ""
	}
	out := string(head)
	if ref, ok := strings.CutPrefix(strings.TrimSpace(out), "ref: "); ok && !strings.Contains(ref, "..") {
		if tip, err := os.ReadFile(filepath.Join(".git", filepath.FromSlash(ref))); err == nil {
			out += string(tip)
		}
	}
	if packed, err := os.ReadFile(filepath.Join(".git", "packed-refs")); err == nil {
		out += string(packed)
	}
	return out
}

func dockerArch(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.Architecture}}").Output()
	if err != nil {
		return "", errors.New("docker info failed: start Docker, then run make dev again")
	}
	switch strings.TrimSpace(string(out)) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("docker runs on %q, which make dev doesn't build for", strings.TrimSpace(string(out)))
}

func cmd(ctx context.Context, env []string, name string, args ...string) error {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = append(os.Environ(), env...)
	return c.Run()
}

func openPage(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if path, err := exec.LookPath(name); err == nil {
		_ = exec.Command(path, url).Start()
	}
}

func say(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "dev: "+format+"\n", args...)
}
