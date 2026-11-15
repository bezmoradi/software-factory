package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"
)

const personFile = "person.json"

var personID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type personJSON struct {
	Version int       `json:"version"`
	ID      string    `json:"id"`
	Made    time.Time `json:"made"`
}

var (
	personMu sync.Mutex
	persons  = map[string]string{}
	local    string
)

func LocalPerson(dir string) (string, error) {
	personMu.Lock()
	defer personMu.Unlock()
	if id, ok := persons[dir]; ok {
		return id, nil
	}
	id, err := loadPerson(dir)
	if err != nil {
		return "", err
	}
	if id == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", errors.New("the engine couldn't make the local person's id")
		}
		id = hex.EncodeToString(b)
		if err := write(dir, personFile, personJSON{Version: 1, ID: id, Made: time.Now().UTC()}); err != nil {
			return "", errors.New("person.json couldn't be written")
		}
	}
	persons[dir] = id
	if local == "" {
		local = id
	}
	return id, nil
}

func adoptPerson(dir string) (string, string) {
	id, err := loadPerson(dir)
	if err != nil || id == "" {
		return "", ""
	}
	personMu.Lock()
	defer personMu.Unlock()
	old := persons[dir]
	persons[dir] = id
	if local == "" || local == old {
		local = id
	}
	return old, id
}

func loadPerson(dir string) (string, error) {
	data, err := read(dir, personFile)
	if err != nil || data == nil {
		return "", err
	}
	var f personJSON
	if err := json.Unmarshal(data, &f); err != nil || f.Version != 1 || !personID.MatchString(f.ID) {
		return "", errors.New("person.json isn't a local person this engine reads")
	}
	return f.ID, nil
}

func ownerIn(dir string) string {
	personMu.Lock()
	defer personMu.Unlock()
	if id, ok := persons[dir]; ok {
		return id
	}
	return local
}
