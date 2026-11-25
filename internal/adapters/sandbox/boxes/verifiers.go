package boxes

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	LabelVerifier       = "dev.software-factory.verifier"
	LabelVerifyImage    = "dev.software-factory.verify-image"
	labelOurs           = "dev.software-factory."
	verifierPrefix      = "-verify-"
	verifierOutPrefix   = "-verify-out-"
	verifierOut         = "/out"
	verifierOutVariable = "SF_OUT"
	maxVerifierOut      = 128 << 20
	ReasonNoBrowser     = "no-browser"
	ReasonRunning       = "running"
	verifierUser        = "10006:10006"
	verifierIn          = "/in"
	verifierWork        = "/work"
	verifierHome        = "/work/home"
	volumeVerify        = "verify"
	verifierOOMScore    = 800
	maxVerifiers        = 4
	maxImageVolumes     = 8
	imageVolumeTmpfs    = "size=256m,mode=1777,exec"
	verifierTmpTmpfs    = "size=512m,mode=1777,exec"
	verifierLogKeep     = 64 << 10
	verifierLogRead     = 64 << 20
	verifierKillEach    = 15 * time.Second
	verifierScript      = "mkdir -p /work/src /work/home && cp -RP /in/files/. /work/src/ && cd /work/src && exec /bin/sh -c \"$(cat /in/command)\""
	maxVerifierAnswer   = 512 << 10
	verifierStateLength = 32
)

var (
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	imageIDPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	pathName       = regexp.MustCompile(`^(HOME|SHELL|[A-Z][A-Z0-9_]*_(HOME|ROOT)|GOROOT|VIRTUAL_ENV)$`)
	pathListName   = regexp.MustCompile(`^(PATH|GOPATH|PYTHONPATH)$`)
	versionName    = regexp.MustCompile(`^[A-Z][A-Z0-9_]*_VERSION$`)
	localeName     = regexp.MustCompile(`^(LANG|LANGUAGE|LC_[A-Z]+)$`)
	wordShapes     = map[string]*regexp.Regexp{
		"PYTHONUNBUFFERED":        regexp.MustCompile(`^[0-9]{1,3}$`),
		"PYTHONDONTWRITEBYTECODE": regexp.MustCompile(`^[0-9]{1,3}$`),
		"GOTOOLCHAIN":             regexp.MustCompile(`^(local|auto|path|go[0-9]{1,6}(\.[0-9]{1,6}){0,3}((rc|beta)[0-9]{1,3})?(\+(auto|path))?)$`),
	}
	pathValue     = regexp.MustCompile(`^/[A-Za-z0-9._+/-]{0,254}$`)
	pathListValue = regexp.MustCompile(`^/[A-Za-z0-9._+/-]*(:/[A-Za-z0-9._+/-]*)*$`)
	versionValue  = regexp.MustCompile(`^([a-z]{1,4}[-_]?)?[0-9]{1,6}(\.[0-9]{1,6}){0,5}([-+][A-Za-z]{1,8}[0-9]{0,3})?$`)
	localeValue   = regexp.MustCompile(`^[A-Za-z]{1,8}([_-][A-Za-z0-9]{1,8})?(\.[A-Za-z0-9-]{1,16})?(@[A-Za-z0-9]{1,16})?(:[A-Za-z]{1,8}([_-][A-Za-z0-9]{1,8})?(\.[A-Za-z0-9-]{1,16})?)*$`)

	volumePath      = regexp.MustCompile(`^(/[A-Za-z0-9._-]+)+$`)
	verifierReserve = []string{"/", verifierIn, verifierWork, verifierOut, "/tmp", "/proc", "/sys", "/dev", "/etc"}
	browserPaths    = []string{"/usr/bin/chromium", "/usr/bin/chromium-browser", "/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/ms-playwright"}
)

var _ session.VerifierHost = (*Helper)(nil)

type verifyImage struct {
	id         string
	env        []string
	entrypoint []string
	labels     map[string]string
	volumes    []string
}

func verifierName(project, run string) string    { return project + verifierPrefix + run }
func verifierOutName(project, run string) string { return project + verifierOutPrefix + run }
func verifierLock(run string) string             { return "verifier:" + run }

func badVerifier(run, digest string) error {
	if !session.ValidBox(run) {
		return refuse(http.StatusBadRequest, ReasonInput, "a verification run is 16 lowercase hexadecimal digits")
	}
	if digest != "" && !digestPattern.MatchString(digest) {
		return refuse(http.StatusBadRequest, ReasonInput, "an image digest is 64 lowercase hexadecimal digits")
	}
	return nil
}

func (r *recipe) verifierLabels(run, digest string) map[string]string {
	return map[string]string{LabelStack: r.project, LabelVerifier: run, LabelVerifyImage: digest}
}

func verifierEnv(image []string, browser bool) []string {
	out := []string{"HOME=" + verifierHome, "LANG=C.UTF-8", "TERM=dumb", "CI=1"}
	ours := map[string]bool{"HOME": true, "LANG": true, "TERM": true, "CI": true}
	if browser {
		out = append(out, verifierOutVariable+"="+verifierOut)
		ours[verifierOutVariable] = true
	}
	for _, e := range image {
		k, v, _ := strings.Cut(e, "=")
		if ours[k] {
			continue
		}
		if !keptValue(k, v) {
			out = append(out, k+"=")
		}
	}
	return out
}

func keptValue(name, value string) bool {
	if session.SecretValue(value) {
		return false
	}
	switch {
	case pathListName.MatchString(name):
		return len(value) <= 4096 && pathListValue.MatchString(value)
	case pathName.MatchString(name):
		return pathValue.MatchString(value)
	case versionName.MatchString(name):
		return len(value) <= 32 && versionValue.MatchString(value)
	case localeName.MatchString(name):
		return localeValue.MatchString(value)
	}
	if shape, ok := wordShapes[name]; ok {
		return shape.MatchString(value)
	}
	return false
}

func (r *recipe) verifierTmpfs(img verifyImage) map[string]string {
	out := map[string]string{
		verifierWork: "size=" + strconv.FormatInt(r.cfg.VerifyDisk, 10) + ",mode=0700,uid=10006,gid=10006,exec",
		"/tmp":       verifierTmpTmpfs,
	}
	for _, v := range img.volumes {
		out[v] = imageVolumeTmpfs
	}
	return out
}

func (r *recipe) verifierCreate(run, digest, volume, out string, img verifyImage) containerCreateJSON {
	labels := r.verifierLabels(run, digest)
	for k := range img.labels {
		if strings.HasPrefix(k, composeLabels) {
			labels[k] = ""
		}
	}
	c := containerCreateJSON{
		Image:       img.id,
		User:        verifierUser,
		Entrypoint:  []string{"/bin/sh", "-c"},
		Cmd:         []string{verifierScript},
		Env:         verifierEnv(img.env, out != ""),
		Labels:      labels,
		Healthcheck: healthJSON{Test: []string{"NONE"}},
		HostConfig: hostCreateJSON{
			Init:           true,
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			OomScoreAdj:    verifierOOMScore,
			Memory:         r.cfg.VerifyMemory,
			MemorySwap:     r.cfg.VerifyMemory,
			NanoCPUs:       r.cfg.VerifyCPUs,
			PidsLimit:      boxPids,
			RestartPolicy:  restartJSON{Name: "no"},
			LogConfig:      logJSON{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
			NetworkMode:    "none",
			Mounts:         []mountJSON{{Type: "volume", Source: volume, Target: verifierIn, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true, Subpath: run}}},
			Tmpfs:          r.verifierTmpfs(img),
			CgroupnsMode:   "private",
			IpcMode:        ipcPrivate,
			ShmSize:        boxShm,
		},
	}
	if out != "" {
		c.HostConfig.Mounts = append(c.HostConfig.Mounts, mountJSON{Type: "volume", Source: out, Target: verifierOut})
	}
	if r.cfg.Runtime != "" {
		c.HostConfig.Runtime = r.cfg.Runtime
	}
	return c
}

func (r *recipe) verifierGeneric(run, digest string) generic {
	return genericOf(netip.Prefix{}, verifierOutName(r.project, run), "{out}", verifierName(r.project, run), "{name}", run, "{run}", digest, "{digest}")
}

func (r *recipe) verifierView(c containerCreateJSON, img verifyImage, g generic) recipeView {
	pr := *r
	pr.image, pr.imageEnv, pr.imageLabel, pr.entrypoint = img.id, img.env, img.labels, img.entrypoint
	labels := map[string]string{}
	maps.Copy(labels, img.labels)
	for k, v := range c.Labels {
		if k != LabelRecipe {
			labels[k] = v
		}
	}
	v := pr.viewOf(c, labels, g, netip.Prefix{})
	v.Address = ""
	return v
}

func ownedVerifier(project, name string, labels map[string]string) (string, bool) {
	run := labels[LabelVerifier]
	if labels[LabelStack] != project || !session.ValidBox(run) || name != verifierName(project, run) {
		return "", false
	}
	if _, boxed := labels[LabelBox]; boxed {
		return "", false
	}
	if _, reviewer := labels[LabelReviewer]; reviewer {
		return "", false
	}
	return run, validRecipeLabel(labels[LabelRecipe]) && digestPattern.MatchString(labels[LabelVerifyImage]) && composeBlank(labels)
}

func verifierLabelled(project string, labels map[string]string) (string, bool) {
	run := labels[LabelVerifier]
	return run, labels[LabelStack] == project && session.ValidBox(run)
}

func (h *Helper) logVerifier(ctx context.Context, action, run, digest, recipe string, err error) {
	fields := []any{"action", action, "run", run}
	if digest != "" {
		fields = append(fields, "digest", digest)
	}
	if recipe != "" {
		fields = append(fields, "recipe", recipe)
	}
	if err == nil {
		shared.For(ctx, h.logger).Info("verifier", append(fields, "outcome", "ok")...)
		return
	}
	reason, message := ReasonDocker, oneLine(err.Error())
	if r, ok := errors.AsType[*Refusal](err); ok {
		reason = r.Reason
	}
	shared.For(ctx, h.logger).Warn("verifier", append(fields, "outcome", "refused", "reason", reason, "message", message)...)
}

func (h *Helper) StartVerifier(ctx context.Context, run, digest string, browser bool) (session.VerifierInfo, error) {
	if err := badVerifier(run, digest); err != nil {
		return session.VerifierInfo{}, err
	}
	if digest == "" {
		return session.VerifierInfo{}, refuse(http.StatusBadRequest, ReasonInput, "a verification run needs the digest of its project's image")
	}
	unlock := h.locks.lock(verifierLock(run))
	defer unlock()
	info, err := h.startVerifier(ctx, run, digest, browser)
	h.logVerifier(ctx, "start", run, digest, info.Recipe, err)
	return info, err
}

func (h *Helper) startVerifier(ctx context.Context, run, digest string, browser bool) (session.VerifierInfo, error) {
	volume, err := h.stackVolume(ctx, volumeVerify)
	if err != nil {
		return session.VerifierInfo{}, err
	}
	if err := h.noVerifyStrays(ctx, volume); err != nil {
		return session.VerifierInfo{}, err
	}
	if err := h.teardownVerifier(ctx, run); err != nil {
		return session.VerifierInfo{}, err
	}
	if err := h.runtimeReady(ctx); err != nil {
		return session.VerifierInfo{}, err
	}
	img, err := h.verifyImage(ctx, digest)
	if err != nil {
		return session.VerifierInfo{}, err
	}
	release, err := h.reserveVerifier(ctx, run)
	if err != nil {
		return session.VerifierInfo{}, err
	}
	defer release()
	out := ""
	if browser {
		if out, err = h.verifierOutVolume(ctx, run, digest); err != nil {
			return session.VerifierInfo{}, err
		}
	}
	name := verifierName(h.rec.project, run)
	g := h.rec.verifierGeneric(run, digest)
	body := h.rec.verifierCreate(run, digest, volume, out, img)
	want := h.rec.verifierView(body, img, g)
	recipe := hashOf(want)
	body.Labels[LabelRecipe] = recipe
	info := session.VerifierInfo{Run: run, Image: img.id, Digest: digest, Limit: time.Duration(h.cfg.VerifyMinutes) * time.Minute, Recipe: recipe}
	var created createdJSON
	if err := h.docker.post(ctx, "/containers/create", url.Values{"name": {name}}, body, &created); err != nil {
		var de *DockerError
		if errors.As(err, &de) && de.Code == http.StatusConflict {
			return info, refuse(http.StatusConflict, ReasonNamed, "a container named %s already exists, so the helper makes no verification container with that name", name)
		}
		return info, verifyFolderOr(dockerFailed("make the verification container", err), err, run)
	}
	fail := func(r *Refusal) (session.VerifierInfo, error) {
		out := *r
		out.Message += h.dropVerifier(ctx, created.ID)
		return info, &out
	}
	if len(created.Warnings) > 0 {
		return fail(refuse(http.StatusInternalServerError, ReasonRecipe, "Docker made the verification container with warnings (%s)", oneLine(strings.Join(created.Warnings, "; "))))
	}
	check := func(when string) *Refusal {
		var got containerInspectJSON
		if err := h.docker.get(ctx, "/containers/"+pathPart(created.ID)+"/json", nil, &got); err != nil {
			return dockerFailed("inspect the verification container "+when, err)
		}
		diffs := differences(want, inspectedIn(got, g, "none"))
		if len(got.HostConfig.Ulimits) > 0 {
			diffs = append(diffs, "Ulimits")
		}
		if len(got.Mounts) != len(body.HostConfig.Mounts) {
			diffs = append(diffs, "MountCount")
		}
		if len(diffs) > 0 || got.Config.Labels[LabelRecipe] != recipe || got.Image != img.id {
			return refuse(http.StatusInternalServerError, ReasonRecipe, "The verification container as Docker made it %s differs from the recipe in %s", when, strings.Join(diffs, ", "))
		}
		return nil
	}
	if r := check("before it started"); r != nil {
		return fail(r)
	}
	if browser {
		if r := h.browserIn(ctx, created.ID, img.id); r != nil {
			return fail(r)
		}
	}
	if err := h.docker.post(ctx, "/containers/"+pathPart(created.ID)+"/start", nil, nil, nil); err != nil {
		return fail(verifyFolderOr(dockerFailed("start the verification container", err), err, run))
	}
	if r := check("after it started"); r != nil {
		return fail(r)
	}
	return info, nil
}

func verifyFolderOr(fallback *Refusal, err error, run string) *Refusal {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "subpath") || strings.Contains(msg, "no such file or directory") {
		return refuse(http.StatusConflict, ReasonFolders, "the run's folder %s must exist in the verify volume before its container starts, and the engine writes it (%s)", run, oneLine(err.Error()))
	}
	return fallback
}

func (h *Helper) dropVerifier(ctx context.Context, id string) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupFor)
	defer cancel()
	if err := h.removeVerifierContainer(ctx, id); err != nil {
		return ", and the helper couldn't remove the container it made: " + oneLine(err.Error())
	}
	return ", so the helper removed the container it made"
}

func (h *Helper) removeVerifierContainer(ctx context.Context, id string) error {
	if err := h.docker.delete(ctx, "/containers/"+pathPart(id), url.Values{"force": {"true"}, "v": {"true"}}); err != nil && !IsNotFound(err) {
		return dockerFailed("remove the verification container", err)
	}
	return nil
}

func (h *Helper) verifyImage(ctx context.Context, digest string) (verifyImage, error) {
	var list []imageListJSON
	if err := h.docker.get(ctx, "/images/json", url.Values{"digests": {"true"}}, &list); err != nil {
		return verifyImage{}, dockerFailed("list its images", err)
	}
	var ids []string
	for _, im := range list {
		match := im.ID == "sha256:"+digest
		for _, d := range im.RepoDigests {
			if strings.HasSuffix(d, "@sha256:"+digest) {
				match = true
			}
		}
		if match && imageIDPattern.MatchString(im.ID) && !slices.Contains(ids, im.ID) {
			ids = append(ids, im.ID)
		}
	}
	switch len(ids) {
	case 0:
		return verifyImage{}, refuse(http.StatusConflict, ReasonImage, "no image with the digest sha256:%s is on this Docker; pull it on the Docker host with docker pull and its name pinned by that digest, then verify again", digest)
	case 1:
	default:
		return verifyImage{}, refuse(http.StatusConflict, ReasonImage, "%d images on this Docker match the digest sha256:%s, so the helper can't tell which to run", len(ids), digest)
	}
	var img imageInspectJSON
	if err := h.docker.get(ctx, "/images/"+pathPart(ids[0])+"/json", nil, &img); err != nil {
		return verifyImage{}, dockerFailed("inspect the image "+ids[0], err)
	}
	if img.ID != ids[0] {
		return verifyImage{}, refuse(http.StatusConflict, ReasonImage, "Docker's record of the image %s changed while the helper read it", ids[0])
	}
	if img.Os != "" && img.Os != "linux" {
		return verifyImage{}, refuse(http.StatusConflict, ReasonImage, "the image %s is for %s, and a verification run needs a Linux image", ids[0], oneLine(img.Os))
	}
	for k := range img.Config.Labels {
		if strings.HasPrefix(k, labelOurs) {
			return verifyImage{}, refuse(http.StatusConflict, ReasonImage, "the image %s carries the label %s, which only the box helper sets, so the helper runs nothing from it", ids[0], oneLine(k))
		}
	}
	out := verifyImage{id: img.ID, env: img.Config.Env, entrypoint: img.Config.Entrypoint, labels: img.Config.Labels}
	for v := range img.Config.Volumes {
		p := path.Clean(v)
		if p != v || !volumePath.MatchString(p) || reserved(p) {
			return verifyImage{}, refuse(http.StatusConflict, ReasonImage, "the image %s declares the volume %s, which a verification container can't cover with a scratch folder", ids[0], oneLine(v))
		}
		out.volumes = append(out.volumes, p)
	}
	if len(out.volumes) > maxImageVolumes {
		return verifyImage{}, refuse(http.StatusConflict, ReasonImage, "the image %s declares %d volumes, more than the %d a verification container covers", ids[0], len(out.volumes), maxImageVolumes)
	}
	slices.Sort(out.volumes)
	return out, nil
}

func reserved(p string) bool {
	for _, r := range verifierReserve {
		if p == r || r != "/" && strings.HasPrefix(p, r+"/") || strings.HasPrefix(r, p+"/") {
			return true
		}
	}
	return false
}

func (h *Helper) noVerifyStrays(ctx context.Context, volume string) error {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", filters(map[string][]string{"volume": {volume}}), &list); err != nil {
		return dockerFailed("list the containers that mount the verify volume", err)
	}
	var found []string
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		if composeMade(c.Labels, h.rec.project, engineService) {
			continue
		}
		if _, ok := ownedVerifier(h.rec.project, strings.TrimPrefix(name, "/"), c.Labels); ok {
			continue
		}
		found = append(found, oneLine(strings.TrimPrefix(name, "/")))
	}
	if len(found) == 0 {
		return nil
	}
	slices.Sort(found)
	shown := found
	if len(shown) > maxNamed {
		shown = shown[:maxNamed]
	}
	return refuse(http.StatusConflict, ReasonShared, "%d other containers mount the stack's verify volume (%s), where every verification run's files live, so the box helper starts no verification container until they are gone (docker rm -f with its name)", len(found), strings.Join(shown, ", "))
}

func (h *Helper) reserveVerifier(ctx context.Context, run string) (func(), error) {
	h.global.Lock()
	defer h.global.Unlock()
	runs, err := h.verifierRuns(ctx, false)
	if err != nil {
		return nil, err
	}
	n := 0
	for r := range runs {
		if r != run {
			n++
		}
	}
	for r := range h.verifying {
		if r != run {
			n++
		}
	}
	if n >= maxVerifiers {
		return nil, refuse(http.StatusConflict, ReasonLimit, "%d verification containers are running or starting, the most the box helper runs at once", n)
	}
	h.verifying[run] = true
	return func() {
		h.global.Lock()
		delete(h.verifying, run)
		h.global.Unlock()
	}, nil
}

func (h *Helper) verifierRuns(ctx context.Context, all bool) (map[string]string, error) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": h.stackFilter()["filters"]}, &list); err != nil {
		return nil, dockerFailed("list the stack's verification containers", err)
	}
	out := map[string]string{}
	for _, c := range list {
		run, ok := verifierLabelled(h.rec.project, c.Labels)
		if !ok {
			continue
		}
		if !all {
			switch c.State {
			case "exited", "dead", "removing", "created":
				continue
			}
		}
		if prev, seen := out[run]; !seen || prev != stateRunning {
			out[run] = c.State
		}
	}
	return out, nil
}

func (h *Helper) verifierContainer(ctx context.Context, run string) (*containerInspectJSON, error) {
	name := verifierName(h.rec.project, run)
	var ci containerInspectJSON
	if err := h.docker.get(ctx, "/containers/"+pathPart(name)+"/json", nil, &ci); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, dockerFailed("inspect the container "+name, err)
	}
	if got, ok := ownedVerifier(h.rec.project, strings.TrimPrefix(ci.Name, "/"), ci.Config.Labels); !ok || got != run {
		return nil, refuse(http.StatusConflict, ReasonNamed, "a container named %s exists without the box helper's labels, so the helper leaves it alone and reads no verification result from it", name)
	}
	return &ci, nil
}

func (h *Helper) teardownVerifier(ctx context.Context, run string) error {
	ci, err := h.verifierContainer(ctx, run)
	if err != nil {
		return err
	}
	if ci != nil {
		if err := h.removeVerifierContainer(ctx, ci.ID); err != nil {
			return err
		}
	}
	var list []containerListJSON
	q := filters(map[string][]string{"label": {LabelStack + "=" + h.rec.project, LabelVerifier + "=" + run}})
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": q["filters"]}, &list); err != nil {
		return dockerFailed("list the containers labelled as the verification run "+run, err)
	}
	for _, c := range list {
		if got, ok := verifierLabelled(h.rec.project, c.Labels); ok && got == run {
			if err := h.removeVerifierContainer(ctx, c.ID); err != nil {
				return err
			}
		}
	}
	return h.dropVerifierOut(ctx, run)
}

func dockerTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 2000 {
		return time.Time{}
	}
	return t
}

func (h *Helper) VerifierState(ctx context.Context, run string) (session.VerifierState, error) {
	if err := badVerifier(run, ""); err != nil {
		return session.VerifierState{}, err
	}
	ci, err := h.verifierContainer(ctx, run)
	if err != nil {
		return session.VerifierState{}, err
	}
	if ci == nil {
		return session.VerifierState{}, refuse(http.StatusNotFound, ReasonUnknown, "the box helper has no verification container for the run %s", run)
	}
	st := session.VerifierState{
		Run: run, State: oneLine(ci.State.Status), ExitCode: ci.State.ExitCode, OOMKilled: ci.State.OOMKilled, Error: oneLine(ci.State.Error),
		StartedAt: dockerTime(ci.State.StartedAt), FinishedAt: dockerTime(ci.State.FinishedAt), Image: ci.Image, Digest: ci.Config.Labels[LabelVerifyImage],
		Limit: time.Duration(h.cfg.VerifyMinutes) * time.Minute,
	}
	if len(st.State) > verifierStateLength {
		st.State = st.State[:verifierStateLength]
	}
	return st, nil
}

func (h *Helper) VerifierLog(ctx context.Context, run string) (string, error) {
	if err := badVerifier(run, ""); err != nil {
		return "", err
	}
	ci, err := h.verifierContainer(ctx, run)
	if err != nil {
		return "", err
	}
	if ci == nil {
		return "", refuse(http.StatusNotFound, ReasonUnknown, "the box helper has no verification container for the run %s", run)
	}
	data, err := h.docker.logTail(ctx, ci.ID, verifierLogKeep, verifierLogRead)
	if err != nil {
		return "", dockerFailed("read the verification container's output", err)
	}
	return strings.ToValidUTF8(string(data), "�"), nil
}

func (h *Helper) RemoveVerifier(ctx context.Context, run string) error {
	if err := badVerifier(run, ""); err != nil {
		return err
	}
	unlock := h.locks.lock(verifierLock(run))
	defer unlock()
	err := h.teardownVerifier(ctx, run)
	h.logVerifier(ctx, "remove", run, "", "", err)
	return err
}

func (h *Helper) ListVerifiers(ctx context.Context) ([]session.BoxInfo, error) {
	runs, err := h.verifierRuns(ctx, true)
	if err != nil {
		return nil, err
	}
	outs, err := h.verifierOuts(ctx)
	if err != nil {
		return nil, err
	}
	for _, run := range outs {
		if _, seen := runs[run]; !seen {
			runs[run] = "volume"
		}
	}
	out := make([]session.BoxInfo, 0, len(runs))
	for run, state := range runs {
		out = append(out, session.BoxInfo{Box: run, State: oneLine(state)})
	}
	slices.SortFunc(out, func(a, b session.BoxInfo) int { return strings.Compare(a.Box, b.Box) })
	return out, nil
}

func (h *Helper) SweepVerifiers(ctx context.Context, dry bool) {
	list, err := h.ListVerifiers(ctx)
	if err != nil {
		h.logger.Warn("verifier sweep", "event", "list_failed", "error", oneLine(err.Error()))
		return
	}
	for _, r := range list {
		if dry {
			h.logger.Info("verifier sweep", "event", "would_remove", "run", r.Box, "state", r.State)
			continue
		}
		unlock := h.locks.lock(verifierLock(r.Box))
		err := h.teardownVerifier(ctx, r.Box)
		unlock()
		h.logVerifier(ctx, "sweep", r.Box, "", "", err)
	}
}

func (h *Helper) killOverdue(ctx context.Context) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", h.stackFilter(), &list); err != nil {
		return
	}
	limit := time.Duration(h.cfg.VerifyMinutes) * time.Minute
	for _, c := range list {
		run, ok := verifierLabelled(h.rec.project, c.Labels)
		if !ok || c.State != stateRunning {
			continue
		}
		var ci containerInspectJSON
		if err := h.docker.get(ctx, "/containers/"+pathPart(c.ID)+"/json", nil, &ci); err != nil {
			continue
		}
		started := dockerTime(ci.State.StartedAt)
		if !ci.State.Running || started.IsZero() || time.Since(started) <= limit {
			continue
		}
		err := h.docker.post(ctx, "/containers/"+pathPart(c.ID)+"/kill", url.Values{"signal": {"KILL"}}, nil, nil)
		if err != nil && IsNotFound(err) {
			err = nil
		}
		h.logVerifier(ctx, "kill_overdue", run, "", ci.Config.Labels[LabelRecipe], err)
	}
}

func (h *Helper) watchVerifiers(ctx context.Context) {
	t := time.NewTicker(verifierKillEach)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.killOverdue(ctx)
			h.killOverdueActions(ctx)
		}
	}
}

func (h *Helper) verifierOutLabels(run, digest string) map[string]string {
	return map[string]string{LabelStack: h.rec.project, LabelVerifier: run, LabelVerifyImage: digest}
}

func ownedVerifierOut(project, run string, v volumeJSON) bool {
	return v.Name == verifierOutName(project, run) && v.Labels[LabelStack] == project && v.Labels[LabelVerifier] == run && v.Driver == volumeDriver && len(v.Options) == 0
}

func (h *Helper) verifierOutVolume(ctx context.Context, run, digest string) (string, error) {
	name := verifierOutName(h.rec.project, run)
	var v volumeJSON
	if err := h.docker.post(ctx, "/volumes/create", nil, volumeCreateJSON{Name: name, Driver: volumeDriver, Labels: h.verifierOutLabels(run, digest)}, &v); err != nil {
		return "", dockerFailed("make the volume "+name, err)
	}
	if !ownedVerifierOut(h.rec.project, run, v) || v.Labels[LabelVerifyImage] != digest {
		return "", refuse(http.StatusConflict, ReasonNamed, "a volume named %s exists without the box helper's labels or with another driver or options, so the helper leaves it alone and starts no browser run with that name", name)
	}
	return name, nil
}

func (h *Helper) dropVerifierOut(ctx context.Context, run string) error {
	name := verifierOutName(h.rec.project, run)
	var v volumeJSON
	if err := h.docker.get(ctx, "/volumes/"+pathPart(name), nil, &v); err != nil {
		if IsNotFound(err) {
			return nil
		}
		return dockerFailed("inspect the volume "+name, err)
	}
	if v.Labels[LabelStack] != h.rec.project || v.Labels[LabelVerifier] != run {
		return refuse(http.StatusConflict, ReasonNamed, "a volume named %s exists without the box helper's labels, so the helper leaves it alone", name)
	}
	if err := h.docker.delete(ctx, "/volumes/"+pathPart(name), nil); err != nil && !IsNotFound(err) {
		return dockerFailed("remove the volume "+name, err)
	}
	return nil
}

func (h *Helper) verifierOuts(ctx context.Context) ([]string, error) {
	var list volumeListJSON
	if err := h.docker.get(ctx, "/volumes", h.stackFilter(), &list); err != nil {
		return nil, dockerFailed("list the stack's volumes", err)
	}
	var out []string
	for _, v := range list.Volumes {
		run := v.Labels[LabelVerifier]
		if session.ValidBox(run) && v.Labels[LabelStack] == h.rec.project && v.Name == verifierOutName(h.rec.project, run) {
			out = append(out, run)
		}
	}
	return out, nil
}

func (h *Helper) browserIn(ctx context.Context, id, image string) *Refusal {
	for _, p := range browserPaths {
		resp, err := h.docker.send(ctx, h.docker.client, http.MethodHead, "/containers/"+pathPart(id)+"/archive", url.Values{"path": {p}}, nil, true)
		if err != nil {
			return dockerFailed("look for a browser in the verification container", err)
		}
		_ = resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK:
			return nil
		case http.StatusNotFound:
		default:
			return refuse(http.StatusBadGateway, ReasonDocker, "Docker answered HTTP %d when the helper looked for a browser in the verification container", resp.StatusCode)
		}
	}
	return refuse(http.StatusConflict, ReasonNoBrowser, "the image %s has no browser at %s, so a browser run has nothing to drive; name an image that holds one, such as the stack's browser stage or Playwright's image", image, strings.Join(browserPaths, ", "))
}

type limitedBody struct {
	io.Reader
	io.Closer
}

func (h *Helper) VerifierOut(ctx context.Context, run string) (io.ReadCloser, error) {
	if err := badVerifier(run, ""); err != nil {
		return nil, err
	}
	ci, err := h.verifierContainer(ctx, run)
	if err != nil {
		return nil, err
	}
	if ci == nil {
		return nil, refuse(http.StatusNotFound, ReasonUnknown, "the box helper has no verification container for the run %s", run)
	}
	if ci.State.Running || ci.State.Status != "exited" {
		return nil, refuse(http.StatusConflict, ReasonRunning, "the verification container of the run %s hasn't exited, so the helper reads none of its files", run)
	}
	if !slices.ContainsFunc(ci.Mounts, func(m mountPointJSON) bool {
		return m.Destination == verifierOut && m.Type == "volume" && m.Name == verifierOutName(h.rec.project, run)
	}) {
		return nil, refuse(http.StatusConflict, ReasonRecipe, "the verification container of the run %s has no %s volume, so it isn't a browser run", run, verifierOut)
	}
	resp, err := h.docker.send(ctx, h.docker.stream, http.MethodGet, "/containers/"+pathPart(ci.ID)+"/archive", url.Values{"path": {verifierOut}}, nil, true)
	if err != nil {
		return nil, dockerFailed("read the verification container's "+verifierOut, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, dockerFailed("read the verification container's "+verifierOut, readDockerError(resp))
	}
	return limitedBody{Reader: io.LimitReader(resp.Body, maxVerifierOut), Closer: resp.Body}, nil
}
