package session

import "time"

const (
	AwayNote      = "the person is away; continue without this or stop"
	awayAgentNote = "While the person was away, Software Factory denied one or more of your permission requests automatically: " + AwayNote + "."
)

func (r *Registry) freshCard(ev map[string]any) bool {
	id, _ := ev["id"].(string)
	return r.cards[id] == nil
}

func (r *Registry) awayAnswer(sid string, ev map[string]any, now time.Time) bool {
	id, _ := ev["id"].(string)
	k := r.cards[id]
	if k == nil || k.kind != "permission" || k.answered || k.away || ev["readOnly"] == true || !r.halt.awayFor(sid) {
		return false
	}
	value := map[string]any{"choice": "no", "note": AwayNote}
	k.away = true
	if k.waiter != nil {
		w := k.waiter
		k.waiter = nil
		w.answer = value
		w.finish(waitDelivered)
		delete(r.cards, id)
	} else {
		k.answer = value
		k.answered = true
		k.at = now
	}
	r.halt.answered(sid, k, value, true)
	return true
}

func (r *Registry) markAway(frame Fields, denied bool) {
	id, _ := frame["id"].(string)
	if k := r.cards[id]; denied || k != nil && k.away {
		frame["away"] = true
	}
}

func awayAnswered(answer any) bool {
	m, _ := answer.(map[string]any)
	return m["choice"] == "no" && m["note"] == AwayNote
}
