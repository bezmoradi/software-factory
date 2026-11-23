package session

import (
	"math/rand/v2"
	"strconv"
	"strings"
)

const nameTries = 20

var nameAdjectives = [...]string{
	"agile", "amber", "arcane", "astute", "audacious", "azure", "balanced", "beaming", "bold", "brave",
	"bright", "brilliant", "calm", "careful", "celestial", "cheerful", "clever", "cosmic", "crafty", "curious",
	"daring", "dazzling", "deep", "determined", "dynamic", "eager", "electric", "elegant", "emerald", "energetic",
	"epic", "fearless", "fierce", "focused", "fortunate", "friendly", "frosty", "gentle", "glowing", "golden",
	"graceful", "grand", "happy", "heroic", "hidden", "honest", "humble", "infinite", "ingenious", "inspired",
	"keen", "kind", "lively", "lucky", "luminous", "magnetic", "majestic", "mighty", "mindful", "mystic",
	"nimble", "noble", "optimal", "patient", "peaceful", "playful", "polished", "powerful", "precise", "primal",
	"radiant", "rapid", "reliable", "resilient", "robust", "rugged", "savvy", "serene", "sharp", "silent",
	"silver", "sleek", "smart", "soaring", "solid", "spirited", "steady", "stellar", "strategic", "subtle",
	"sunny", "swift", "tenacious", "tranquil", "unified", "valiant", "vibrant", "vivid", "wise", "zen",
}

var nameNouns = [...]string{
	"albatross", "anvil", "badger", "beacon", "bison", "bluejay", "boulder", "bridge", "canyon", "captain",
	"cascade", "castle", "cheetah", "circuit", "comet", "compass", "condor", "coral", "cougar", "crystal",
	"dolphin", "dragon", "eagle", "eclipse", "falcon", "feather", "ferret", "firefly", "fjord", "flamingo",
	"forge", "fox", "gazelle", "gecko", "glacier", "glider", "griffin", "hammer", "harbor", "heron",
	"horizon", "ibex", "island", "jaguar", "kestrel", "kingfisher", "lantern", "leopard", "lightning", "lynx",
	"marble", "meadow", "meteor", "monarch", "mongoose", "mountain", "nebula", "nightingale", "oasis", "octopus",
	"orchid", "otter", "owl", "panther", "paradox", "phoenix", "pioneer", "planet", "prism", "puma",
	"quartz", "raven", "reef", "rocket", "sailor", "satellite", "scarab", "scout", "shark", "sparrow",
	"sphinx", "spider", "squirrel", "starling", "summit", "thunder", "tiger", "tortoise", "trident", "turbine",
	"turtle", "vanguard", "voyager", "walrus", "warbler", "whale", "wolf", "wombat", "zenith", "zephyr",
}

func nameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func pickName(taken map[string]bool) string {
	name := ""
	for range nameTries {
		name = nameAdjectives[rand.IntN(len(nameAdjectives))] + "-" + nameNouns[rand.IntN(len(nameNouns))]
		if !taken[name] {
			return name
		}
	}
	for n := 2; ; n++ {
		if numbered := name + "-" + strconv.Itoa(n); !taken[numbered] {
			return numbered
		}
	}
}

func (r *Registry) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.admitted))
	for _, a := range r.admitted {
		out = append(out, a.Name)
	}
	return out
}

func (d *Dispatcher) SessionName() string {
	taken := map[string]bool{}
	d.mu.Lock()
	for _, r := range d.runs {
		taken[nameKey(r.display)] = true
	}
	for _, r := range d.waiting {
		taken[nameKey(r.display)] = true
	}
	d.mu.Unlock()
	for _, name := range d.reg.names() {
		taken[nameKey(name)] = true
	}
	return pickName(taken)
}
