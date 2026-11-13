package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"maps"
	"math/big"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	root := env("GITSERVER_ROOT", "/srv/git")
	var err error
	switch {
	case len(os.Args) == 1:
		err = serve(root)
	case len(os.Args) == 3 && os.Args[1] == "commit":
		var h string
		h, err = commit(root, os.Args[2])
		if err == nil {
			fmt.Println(h)
		}
	case len(os.Args) == 2 && os.Args[1] == "commit":
		err = errUsage
	default:
		err = apiCommand(root, os.Args[1:])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Dev Git Server", "GIT_AUTHOR_EMAIL=dev@gitserver.test", "GIT_COMMITTER_NAME=Dev Git Server", "GIT_COMMITTER_EMAIL=dev@gitserver.test"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

func commit(root, text string) (string, error) {
	bare := filepath.Join(root, "demo.git")
	work, err := os.MkdirTemp("", "gitserver-work")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(work) }()
	if _, err := os.Stat(bare); errors.Is(err, os.ErrNotExist) {
		if _, err := git(root, "init", "-q", "--bare", "-b", "main", "demo.git"); err != nil {
			return "", err
		}
		if _, err := git(work, "init", "-q", "-b", "main"); err != nil {
			return "", err
		}
		if _, err := git(work, "remote", "add", "origin", bare); err != nil {
			return "", err
		}
	} else if _, err := git(work, "clone", "-q", bare, "."); err != nil {
		return "", err
	}
	f, err := os.OpenFile(filepath.Join(work, "README.md"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(f, "- %s (%s)\n", text, time.Now().UTC().Format(time.RFC3339))
	_ = f.Close()
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-q", "-m", text}, {"push", "-q", "origin", "HEAD:main"}} {
		if _, err := git(work, args...); err != nil {
			return "", err
		}
	}
	return git(work, "rev-parse", "HEAD")
}

func certificates(dir string, names []string) (tls.Certificate, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Software Factory development git CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	caCert, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmp := filepath.Join(dir, ".ca.pem.tmp")
	if err := os.WriteFile(tmp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.Rename(tmp, filepath.Join(dir, "ca.pem")); err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}, nil
}

func serve(root string) error {
	token := os.Getenv("GITSERVER_TOKEN")
	if token == "" {
		return errors.New("set GITSERVER_TOKEN")
	}
	if _, err := os.Stat(filepath.Join(root, "demo.git")); errors.Is(err, os.ErrNotExist) {
		if _, err := commit(root, "first commit"); err != nil {
			return err
		}
		log.Print("seeded demo.git")
	}
	cert, err := certificates(env("GITSERVER_CA_DIR", "/srv/ca"), strings.Split(env("GITSERVER_NAMES", "gitserver.test,gitserver,localhost"), ","))
	if err != nil {
		return err
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1", "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1",
		"REMOTE_USER=sf-dev"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.Handle("/api/v3/", api{root: root, token: token, repo: "demo"})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if _, pass, ok := r.BasicAuth(); !ok || pass != token {
			log.Printf("%s %s refused: no or wrong token", r.Method, r.URL.Path)
			w.Header().Set("WWW-Authenticate", `Basic realm="sf-gitserver"`)
			http.Error(w, "a token is needed", http.StatusUnauthorized)
			return
		}
		log.Printf("%s %s", r.Method, r.URL.Path)
		if rest, ok := strings.CutPrefix(r.URL.Path, "/"+devOwner+"/demo.git"); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
			r.URL.Path = "/demo.git" + rest
			r.URL.RawPath = ""
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			receivePack(w, r, root, backend)
			return
		}
		backend.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: env("GITSERVER_LISTEN", ":8443"), Handler: mux, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}}
	log.Printf("serving https on %s", srv.Addr)
	return srv.ListenAndServeTLS("", "")
}

type held struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (h *held) Header() http.Header         { return h.header }
func (h *held) Write(b []byte) (int, error) { return h.body.Write(b) }
func (h *held) WriteHeader(status int)      { h.status = status }

func receivePack(w http.ResponseWriter, r *http.Request, root string, backend http.Handler) {
	delay, after := pushDelay(root)
	wait := func(when string) bool {
		if delay == 0 {
			return true
		}
		log.Printf("receive-pack delayed %d seconds %s applying the push", delay, when)
		select {
		case <-time.After(time.Duration(delay) * time.Second):
			return true
		case <-r.Context().Done():
			log.Printf("receive-pack: the client left during the delay %s applying the push", when)
			return false
		}
	}
	if !after {
		if wait("before") {
			backend.ServeHTTP(w, r)
		}
		return
	}
	h := &held{header: http.Header{}, status: http.StatusOK}
	backend.ServeHTTP(h, r)
	log.Printf("receive-pack applied, status %d", h.status)
	if !wait("after") {
		return
	}
	maps.Copy(w.Header(), h.header)
	w.WriteHeader(h.status)
	_, _ = w.Write(h.body.Bytes())
}
