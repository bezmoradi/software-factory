package session

func (w *Work) AutoFindings(project string) bool {
	return w.load().Projects[project].Findings
}

func (w *Work) SetAutoFindings(path string, on bool) error {
	r, err := w.repoByPath(path)
	if err != nil {
		return err
	}
	return w.change(func(st *WorkState) error {
		p := st.Projects[r.Name]
		p.Findings = on
		st.Projects[r.Name] = p
		return nil
	})
}
