package boxes

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	LabelReviewer     = "dev.software-factory.reviewer"
	LabelReviewerHome = "dev.software-factory.reviewer-home"
	reviewerPrefix    = "-reviewer-"
	reviewerHomeName  = "-reviewer-home"
	reviewerUser      = "10004:10004"
	reviewerHomeAt    = "/home/reviewer"
	ReviewsAt         = "/reviews"
	reviewerProxyURL  = "http://egress-review:3128"
	volumeReviews     = "reviews"
	reviewerHomeLock  = "reviewer-home"
	labelTrue         = "true"
	maxReviewers      = 32
)

type ReviewerMaker interface {
	StartReviewer(ctx context.Context, run, snapshot string) (session.BoxInfo, error)
	RemoveReviewer(ctx context.Context, run string) error
	ListReviewers(ctx context.Context) ([]session.BoxInfo, error)
}

var _ ReviewerMaker = (*Helper)(nil)

type reviewerVolumes struct {
	Reviews, Agents, DevBin string
}

func reviewerName(project, run string) string { return project + reviewerPrefix + run }
func reviewerHome(project string) string      { return project + reviewerHomeName }
func reviewerLock(run string) string          { return "reviewer:" + run }

func badRun(run, snapshot string) error {
	if !session.ValidBox(run) {
		return refuse(http.StatusBadRequest, ReasonInput, "a reviewer run is 16 lowercase hexadecimal digits")
	}
	if snapshot != "" && !session.ValidBox(snapshot) {
		return refuse(http.StatusBadRequest, ReasonInput, "a review snapshot is 16 lowercase hexadecimal digits")
	}
	return nil
}

func (r *recipe) reviewerLabels(run string) map[string]string {
	return map[string]string{LabelStack: r.project, LabelReviewer: run}
}

func reviewerEnv() []string {
	return []string{
		"SF_WORKSPACE=" + ReviewsAt,
		"HOME=" + reviewerHomeAt,
		"SF_AGENT_HOST_LISTEN=:" + agentHostPort,
		"HTTPS_PROXY=" + reviewerProxyURL,
		"HTTP_PROXY=" + reviewerProxyURL,
		"https_proxy=" + reviewerProxyURL,
		"http_proxy=" + reviewerProxyURL,
		"NO_PROXY=" + noProxy,
		"no_proxy=" + noProxy,
		"DISABLE_AUTOUPDATER=1",
	}
}

func (r *recipe) reviewerMounts(snapshot string, vols reviewerVolumes) []mountJSON {
	var out []mountJSON
	if snapshot != "" {
		out = append(out, mountJSON{Type: "volume", Source: vols.Reviews, Target: ReviewsAt + "/" + snapshot, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true, Subpath: snapshot}})
	}
	out = append(out,
		mountJSON{Type: "volume", Source: vols.Agents, Target: agentsTarget, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true}},
		mountJSON{Type: "volume", Source: reviewerHome(r.project), Target: reviewerHomeAt},
	)
	if r.cfg.Live {
		out = append(out, mountJSON{Type: "volume", Source: vols.DevBin, Target: liveTarget, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true}})
	}
	return out
}

func (r *recipe) reviewerCreate(run, snapshot string, subnet netip.Prefix, vols reviewerVolumes) containerCreateJSON {
	net := reviewerName(r.project, run)
	c := containerCreateJSON{
		Image:       r.image,
		User:        reviewerUser,
		Cmd:         []string{"agent-host", "-mode", "review"},
		Env:         reviewerEnv(),
		Labels:      r.blanked(r.reviewerLabels(run)),
		Healthcheck: boxHealth(),
		HostConfig: hostCreateJSON{
			Init:           true,
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			OomScoreAdj:    boxOOMScore,
			Memory:         r.cfg.ReviewerMemory,
			MemorySwap:     r.cfg.ReviewerMemory,
			NanoCPUs:       r.cfg.ReviewerCPUs,
			PidsLimit:      boxPids,
			RestartPolicy:  restartJSON{Name: "no"},
			LogConfig:      logJSON{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
			ExtraHosts:     []string{"egress-review:" + egressOf(subnet).String()},
			NetworkMode:    net,
			Mounts:         r.reviewerMounts(snapshot, vols),
			Tmpfs:          map[string]string{"/tmp": "size=1g,mode=1777"},
			CgroupnsMode:   "private",
			IpcMode:        ipcPrivate,
			ShmSize:        boxShm,
		},
	}
	if r.cfg.Runtime != "" {
		c.HostConfig.Runtime = r.cfg.Runtime
	}
	if r.cfg.Live {
		c.Entrypoint = []string{liveEntrypoint}
	}
	c.NetworkingConfig.EndpointsConfig = map[string]endpointCreateJSON{net: {IPAMConfig: ipamAddressJSON{IPv4Address: agentOf(subnet).String()}}}
	return c
}

func (r *recipe) reviewerGeneric(run, snapshot string, subnet netip.Prefix) generic {
	pairs := []string{reviewerName(r.project, run), "{network}", reviewerHome(r.project), "{home}"}
	if snapshot != "" {
		pairs = append(pairs, snapshot, "{snapshot}")
	}
	return genericOf(subnet, append(pairs, run, "{run}")...)
}

func ownedReviewer(project, name string, labels map[string]string) (string, bool) {
	run := labels[LabelReviewer]
	if labels[LabelStack] != project || !session.ValidBox(run) || name != reviewerName(project, run) {
		return "", false
	}
	if _, boxed := labels[LabelBox]; boxed {
		return "", false
	}
	return run, true
}

func reviewerLabelled(project string, labels map[string]string) (string, bool) {
	run := labels[LabelReviewer]
	return run, labels[LabelStack] == project && session.ValidBox(run)
}

func (h *Helper) ownsReviewer(ctx context.Context, name, image string, labels map[string]string) (string, bool) {
	run, ok := ownedReviewer(h.rec.project, strings.TrimPrefix(name, "/"), labels)
	if !ok || !validRecipeLabel(labels[LabelRecipe]) {
		return "", false
	}
	if composeBlank(labels) {
		return run, true
	}
	il, ok := h.imageLabels(ctx, image)
	if !ok || !composeMatches(labels, il) {
		return "", false
	}
	return run, true
}

func (h *Helper) logReviewer(ctx context.Context, action, run, snapshot, recipe string, err error) {
	fields := []any{"action", action, "run", run}
	if snapshot != "" {
		fields = append(fields, "snapshot", snapshot)
	}
	if recipe != "" {
		fields = append(fields, "recipe", recipe)
	}
	if err == nil {
		shared.For(ctx, h.logger).Info("reviewer", append(fields, "outcome", "ok")...)
		return
	}
	reason, message := ReasonDocker, oneLine(err.Error())
	if r, ok := errors.AsType[*Refusal](err); ok {
		reason = r.Reason
	}
	shared.For(ctx, h.logger).Warn("reviewer", append(fields, "outcome", "refused", "reason", reason, "message", message)...)
}

func (h *Helper) StartReviewer(ctx context.Context, run, snapshot string) (session.BoxInfo, error) {
	if err := badRun(run, snapshot); err != nil {
		return session.BoxInfo{}, err
	}
	unlock := h.locks.lock(reviewerLock(run))
	defer unlock()
	info, err := h.startReviewer(ctx, run, snapshot)
	h.logReviewer(ctx, "start", run, snapshot, info.Recipe, err)
	return info, err
}

func (h *Helper) startReviewer(ctx context.Context, run, snapshot string) (session.BoxInfo, error) {
	vols, err := h.reviewerVolumes(ctx)
	if err != nil {
		return session.BoxInfo{}, err
	}
	if err := h.noReviewStrays(ctx, vols.Reviews); err != nil {
		return session.BoxInfo{}, err
	}
	if err := h.teardownReviewer(ctx, run); err != nil {
		return session.BoxInfo{}, err
	}
	if err := h.reviewerHomeVolume(ctx); err != nil {
		return session.BoxInfo{}, err
	}
	if err := h.runtimeReady(ctx); err != nil {
		return session.BoxInfo{}, err
	}
	engine, err := h.service(ctx, engineService)
	if err != nil {
		return session.BoxInfo{}, err
	}
	egress, err := h.service(ctx, egressService)
	if err != nil {
		return session.BoxInfo{}, err
	}
	subnet, release, err := h.reserveReviewer(ctx, run)
	if err != nil {
		return session.BoxInfo{}, err
	}
	defer release()
	name := reviewerName(h.rec.project, run)
	g := h.rec.reviewerGeneric(run, snapshot, subnet)
	want := h.rec.viewOf(h.rec.reviewerCreate(run, snapshot, subnet, vols), h.rec.wanted(h.rec.blanked(h.rec.reviewerLabels(run))), g, subnet)
	p := plan{
		what: "reviewer", name: name, labels: h.rec.reviewerLabels(run), subnet: subnet,
		want: want, body: h.rec.reviewerCreate(run, snapshot, subnet, vols),
		inspect: func(ci containerInspectJSON) recipeView { return inspectedIn(ci, g, name) },
		read:    func(ctx context.Context) (*networkInspectJSON, error) { return h.reviewerNetwork(ctx, run) },
		owned: func(n *networkInspectJSON) []string {
			if got, ok := ownedReviewer(h.rec.project, n.Name, n.Labels); !ok || got != run {
				return []string{"name or labels"}
			}
			return nil
		},
		refused: func(fallback *Refusal, err error) error { return snapshotOr(fallback, err, snapshot) },
	}
	ready, n, recipe, err := h.assemble(ctx, p, engine, egress)
	if err != nil {
		return session.BoxInfo{Box: run, Recipe: recipe}, err
	}
	return reviewerInfo(ready, n, run, recipe), nil
}

func snapshotOr(fallback *Refusal, err error, snapshot string) error {
	msg := strings.ToLower(err.Error())
	if snapshot != "" && (strings.Contains(msg, "subpath") || strings.Contains(msg, "no such file or directory")) {
		return refuse(http.StatusConflict, ReasonFolders, "the review's snapshot %s must exist in the reviews volume before its reviewer container starts, and the engine writes it (%s)", snapshot, oneLine(err.Error()))
	}
	return fallback
}

func reviewerInfo(ci containerInspectJSON, n *networkInspectJSON, run, recipe string) session.BoxInfo {
	info := session.BoxInfo{Box: run, State: stateOf(ci), Recipe: recipe}
	subnet, ok := boxSubnet(n)
	if !ok {
		return info
	}
	info.Subnet, info.Gateway = subnet, gatewayOf(subnet)
	if len(n.IPAM.Config) == 1 {
		if g, err := netip.ParseAddr(n.IPAM.Config[0].Gateway); err == nil {
			info.Gateway = g
		}
	}
	info.Engine, info.Agent = engineOf(subnet), agentOf(subnet)
	info.Host = net.JoinHostPort(agentOf(subnet).String(), agentHostPort)
	info.Facts = []string{"recipe=verified", "image=" + ci.Image, "address=" + agentOf(subnet).String(), "network-internal=" + strconv.FormatBool(n.Internal)}
	return info
}

func (h *Helper) reviewerVolumes(ctx context.Context) (reviewerVolumes, error) {
	var v reviewerVolumes
	var err error
	if v.Reviews, err = h.stackVolume(ctx, volumeReviews); err != nil {
		return v, err
	}
	if v.Agents, err = h.stackVolume(ctx, volumeAgents); err != nil {
		return v, err
	}
	if h.cfg.Live {
		if v.DevBin, err = h.stackVolume(ctx, volumeDevBin); err != nil {
			return v, err
		}
	}
	return v, nil
}

func reviewerHomeShaped(project string, v volumeJSON) bool {
	return v.Name == reviewerHome(project) && v.Labels[LabelStack] == project && v.Labels[LabelReviewerHome] == labelTrue && v.Driver == volumeDriver && len(v.Options) == 0
}

func (h *Helper) reviewerHomeVolume(ctx context.Context) error {
	unlock := h.locks.lock(reviewerHomeLock)
	defer unlock()
	name := reviewerHome(h.rec.project)
	var v volumeJSON
	err := h.docker.get(ctx, "/volumes/"+pathPart(name), nil, &v)
	switch {
	case err == nil:
	case IsNotFound(err):
		labels := map[string]string{LabelStack: h.rec.project, LabelReviewerHome: labelTrue}
		if err := h.docker.post(ctx, "/volumes/create", nil, volumeCreateJSON{Name: name, Driver: volumeDriver, Labels: labels}, &v); err != nil {
			return dockerFailed("make the volume "+name, err)
		}
		h.logger.Info("reviewer", "action", "make_home", "volume", name, "outcome", "ok")
	default:
		return dockerFailed("inspect the volume "+name, err)
	}
	if !reviewerHomeShaped(h.rec.project, v) {
		return refuse(http.StatusConflict, ReasonNamed, "a volume named %s exists without the box helper's labels or with another driver or options, so the helper leaves it alone and starts no reviewer container", name)
	}
	return nil
}

func (h *Helper) noReviewStrays(ctx context.Context, reviews string) error {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", filters(map[string][]string{"volume": {reviews}}), &list); err != nil {
		return dockerFailed("list the containers that mount the reviews volume", err)
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
		if _, ok := h.ownsReviewer(ctx, name, c.ImageID, c.Labels); ok {
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
	return refuse(http.StatusConflict, ReasonShared, "%d other containers mount the stack's reviews volume (%s), where every review's snapshot lives, so the box helper starts no reviewer container until they are gone: remove them, such as the reviewer container an earlier version ran (docker compose up -d --remove-orphans, or docker rm -f with its name)", len(found), strings.Join(shown, ", "))
}

func (h *Helper) reserveReviewer(ctx context.Context, run string) (netip.Prefix, func(), error) {
	h.global.Lock()
	defer h.global.Unlock()
	live, err := h.reviewerRuns(ctx, false)
	if err != nil {
		return netip.Prefix{}, nil, err
	}
	n := 0
	for r := range live {
		if r != run {
			n++
		}
	}
	for k := range h.pending {
		if strings.HasPrefix(k, reviewerLock("")) && k != reviewerLock(run) {
			n++
		}
	}
	if n >= maxReviewers {
		return netip.Prefix{}, nil, refuse(http.StatusConflict, ReasonLimit, "%d reviewer containers are running or starting, the most the box helper runs at once", n)
	}
	return h.pickLocked(ctx, reviewerLock(run), "reviewer container")
}

func (h *Helper) reviewerRuns(ctx context.Context, all bool) (map[string]string, error) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": h.stackFilter()["filters"]}, &list); err != nil {
		return nil, dockerFailed("list the stack's reviewer containers", err)
	}
	out := map[string]string{}
	for _, c := range list {
		run, ok := reviewerLabelled(h.rec.project, c.Labels)
		if !ok {
			continue
		}
		if !all {
			switch c.State {
			case "exited", "dead", "removing":
				continue
			}
		}
		if prev, seen := out[run]; !seen || prev != stateRunning {
			out[run] = c.State
		}
	}
	return out, nil
}

func (h *Helper) reviewerContainer(ctx context.Context, run string) (*containerInspectJSON, error) {
	name := reviewerName(h.rec.project, run)
	var ci containerInspectJSON
	if err := h.docker.get(ctx, "/containers/"+pathPart(name)+"/json", nil, &ci); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, dockerFailed("inspect the container "+name, err)
	}
	if got, ok := h.ownsReviewer(ctx, ci.Name, ci.Image, ci.Config.Labels); !ok || got != run {
		return nil, refuse(http.StatusConflict, ReasonNamed, "a container named %s exists without the box helper's labels, so the helper leaves it alone and makes no reviewer container with that name", name)
	}
	return &ci, nil
}

func (h *Helper) reviewerNetwork(ctx context.Context, run string) (*networkInspectJSON, error) {
	name := reviewerName(h.rec.project, run)
	var n networkInspectJSON
	if err := h.docker.get(ctx, "/networks/"+pathPart(name), nil, &n); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, dockerFailed("inspect the network "+name, err)
	}
	if got, ok := ownedReviewer(h.rec.project, n.Name, n.Labels); !ok || got != run {
		return nil, refuse(http.StatusConflict, ReasonNamed, "a network named %s exists without the box helper's labels, so the helper leaves it alone and makes no reviewer container with that name", name)
	}
	return &n, nil
}

func (h *Helper) teardownReviewer(ctx context.Context, run string) error {
	ci, err := h.reviewerContainer(ctx, run)
	if err != nil {
		return err
	}
	if ci != nil {
		if err := h.dropContainer(ctx, ci.ID); err != nil {
			return err
		}
	}
	var list []containerListJSON
	q := filters(map[string][]string{"label": {LabelStack + "=" + h.rec.project, LabelReviewer + "=" + run}})
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": q["filters"]}, &list); err != nil {
		return dockerFailed("list the containers labelled as the reviewer run "+run, err)
	}
	for _, c := range list {
		if got, ok := reviewerLabelled(h.rec.project, c.Labels); ok && got == run {
			if err := h.dropContainer(ctx, c.ID); err != nil {
				return err
			}
		}
	}
	n, err := h.reviewerNetwork(ctx, run)
	if err != nil || n == nil {
		return err
	}
	return h.dropNetwork(ctx, n)
}

func (h *Helper) RemoveReviewer(ctx context.Context, run string) error {
	if err := badRun(run, ""); err != nil {
		return err
	}
	unlock := h.locks.lock(reviewerLock(run))
	defer unlock()
	err := h.teardownReviewer(ctx, run)
	h.logReviewer(ctx, "remove", run, "", "", err)
	return err
}

func (h *Helper) ListReviewers(ctx context.Context) ([]session.BoxInfo, error) {
	runs, err := h.reviewerRuns(ctx, true)
	if err != nil {
		return nil, err
	}
	var nets []networkInspectJSON
	if err := h.docker.get(ctx, "/networks", h.stackFilter(), &nets); err != nil {
		return nil, dockerFailed("list the stack's networks", err)
	}
	for _, n := range nets {
		if run, ok := ownedReviewer(h.rec.project, n.Name, n.Labels); ok {
			if _, seen := runs[run]; !seen {
				runs[run] = "network"
			}
		}
	}
	out := make([]session.BoxInfo, 0, len(runs))
	for run, state := range runs {
		out = append(out, session.BoxInfo{Box: run, State: oneLine(state)})
	}
	slices.SortFunc(out, func(a, b session.BoxInfo) int { return strings.Compare(a.Box, b.Box) })
	return out, nil
}

func (h *Helper) SweepReviewers(ctx context.Context, dry bool) {
	list, err := h.ListReviewers(ctx)
	if err != nil {
		h.logger.Warn("reviewer sweep", "event", "list_failed", "error", oneLine(err.Error()))
		return
	}
	for _, r := range list {
		if dry {
			h.logger.Info("reviewer sweep", "event", "would_remove", "run", r.Box, "state", r.State)
			continue
		}
		unlock := h.locks.lock(reviewerLock(r.Box))
		err := h.teardownReviewer(ctx, r.Box)
		unlock()
		h.logReviewer(ctx, "sweep", r.Box, "", "", err)
	}
}

func (h *Helper) reconnectReviewers(ctx context.Context, m stackMembers) {
	runs, err := h.reviewerRuns(ctx, false)
	if err != nil {
		return
	}
	for run, state := range runs {
		if state != stateRunning {
			continue
		}
		unlock := h.locks.lock(reviewerLock(run))
		if n, err := h.reviewerNetwork(ctx, run); err == nil && n != nil {
			if subnet, ok := boxSubnet(n); ok {
				for _, member := range []struct {
					id   string
					addr netip.Addr
				}{{m.engine, engineOf(subnet)}, {m.egress, egressOf(subnet)}} {
					if member.id == "" {
						continue
					}
					if err := h.ensure(ctx, n, member.id, member.addr); err != nil {
						h.logReviewer(ctx, "reconnect", run, "", "", err)
					}
				}
			}
		}
		unlock()
	}
}
