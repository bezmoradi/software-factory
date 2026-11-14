package page

import "github.com/bezmoradi/software-factory/internal/core/session"

type citedJSON struct {
	Looked   []string `json:"looked"`
	NotInOSV []string `json:"notInOsv"`
	Failed   string   `json:"failed,omitempty"`
	More     int      `json:"more,omitempty"`
}

func citedOf(c session.CitedIDs) *citedJSON {
	if len(c.Looked) == 0 {
		return nil
	}
	return &citedJSON{Looked: nonNil(c.Looked), NotInOSV: nonNil(c.Missing), Failed: c.Failed, More: c.More}
}
