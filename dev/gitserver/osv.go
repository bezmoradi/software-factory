package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const osvDownFile = "osv-down"

var osvHeld = map[string]string{
	"CVE-2021-44228":      "Apache Log4j2 JNDI features do not protect against attacker-controlled LDAP endpoints",
	"GHSA-jfh8-c2jp-5v3q": "Remote code injection in Log4j",
}

type osvFake struct {
	root string
}

func (o osvFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutPrefix(r.URL.Path, "/osv/v1/vulns/")
	if r.Method != http.MethodGet || !ok || id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Lstat(filepath.Join(o.root, osvDownFile)); err == nil {
		log.Printf("osv %s: answering 503, %s exists", id, osvDownFile)
		http.Error(w, "the fake OSV is down", http.StatusServiceUnavailable)
		return
	}
	summary, held := osvHeld[id]
	if !held {
		log.Printf("osv %s: not held", id)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":5,"message":"Bug not found."}`))
		return
	}
	log.Printf("osv %s: held", id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "summary": summary, "aliases": []string{}})
}
