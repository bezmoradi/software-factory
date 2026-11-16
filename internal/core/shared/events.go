package shared

import "time"

const MaxEvents = 500

type Event struct {
	Time  time.Time
	Level string
	Part  string
	Kind  string
	Error string
	Req   string
}

type Recent interface {
	Recent() []Event
}
