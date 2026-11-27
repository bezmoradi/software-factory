package session

type KeptText struct {
	Hash string
	Text string
}

type ApprovedTexts struct {
	Requirements KeptText
	Plan         KeptText
}

type ApprovedStore interface {
	Load(sid string) (ApprovedTexts, error)
	Save(sid string, t ApprovedTexts) error
	Drop(sid string) error
	Keep(sids []string) error
}

func keptOf(a *workApproval) KeptText {
	if a == nil || a.Text == "" {
		return KeptText{}
	}
	return KeptText{Hash: a.Hash, Text: a.Text}
}

func (t ApprovedTexts) key() string {
	if t.Requirements.Hash == "" && t.Plan.Hash == "" {
		return ""
	}
	return t.Requirements.Hash + " " + t.Plan.Hash
}

func (w *Work) textsOf(sid string) ApprovedTexts {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.sessions[sid]
	if s == nil {
		return ApprovedTexts{}
	}
	return ApprovedTexts{Requirements: keptOf(s.req), Plan: keptOf(s.plan)}
}

func (w *Work) saveTexts(sid string) {
	if w.cfg.Texts == nil {
		return
	}
	w.textMu.Lock()
	defer w.textMu.Unlock()
	t := w.textsOf(sid)
	key := t.key()
	had, known := w.textKeys[sid]
	if known && had == key || !known && key == "" {
		return
	}
	var err error
	if key == "" {
		err = w.cfg.Texts.Drop(sid)
	} else {
		err = w.cfg.Texts.Save(sid, t)
	}
	switch {
	case err != nil:
		delete(w.textKeys, sid)
		w.cfg.Logger.Warn("work", "event", "approved-texts-not-saved")
	case key == "":
		delete(w.textKeys, sid)
	default:
		w.textKeys[sid] = key
	}
}

func (w *Work) loadTexts(sid string) ApprovedTexts {
	if w.cfg.Texts == nil {
		return ApprovedTexts{}
	}
	t, err := w.cfg.Texts.Load(sid)
	if err != nil {
		w.cfg.Logger.Warn("work", "event", "approved-texts-unreadable")
		return ApprovedTexts{}
	}
	return t
}

func (w *Work) keepTexts(sids []string) {
	if w.cfg.Texts == nil {
		return
	}
	if err := w.cfg.Texts.Keep(sids); err != nil {
		w.cfg.Logger.Warn("work", "event", "approved-texts-not-pruned")
	}
}
