package session

const ProjectPRComment = "prComment"

func (w *Work) SetProjectOption(path, op string, on bool) error {
	r, err := w.repoByPath(path)
	if err != nil {
		return err
	}
	if op != ProjectPRComment {
		return explained(invalid("op"), "The engine doesn't keep that project setting.")
	}
	return w.change(func(st *WorkState) error {
		p := st.Projects[r.Name]
		p.PRComment = on
		st.Projects[r.Name] = p
		return nil
	})
}
