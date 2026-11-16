package session

import "time"

type RateWindow struct {
	Kind        string
	Utilization float64
	ResetsAt    time.Time
	Status      string
}
