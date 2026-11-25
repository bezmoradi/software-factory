package boxes

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	LabelAction     = "dev.software-factory.action"
	actionPrefix    = "-action-"
	actionUser      = "10007:10007"
	actionGroup     = "10000"
	actionIn        = "/in"
	volumeActions   = "actions"
	actionsOptions  = "size=16m,uid=10001,gid=10000,mode=0710"
	actionMemory    = 1 << 30
	actionCPUs      = 1_000_000_000
	actionPids      = 512
	actionHomeTmpfs = "size=64m,mode=0700,uid=10007,gid=10007"
	actionTmpTmpfs  = "size=256m,mode=1777"
	maxActionRuns   = session.MaxActionRuns
	actionKillAfter = (session.MaxActionMinutes + 1) * time.Minute
)

var _ session.ActionHost = (*Helper)(nil)

type actionVolumes struct {
	Actions, Workspace, DevBin string
}

func actionName(project, run string) string { return project + actionPrefix + run }
func actionLock(run string) string          { return "action:" + run }

func badAction(run, box string) error {
	if !session.ValidBox(run) {
		return refuse(http.StatusBadRequest, ReasonInput, "an action run is 16 lowercase hexadecimal digits")
	}
	if box != "" && !session.ValidBox(box) {
		return refuse(http.StatusBadRequest, ReasonInput, "a box is 16 lowercase hexadecimal digits")
	}
	return nil
}

func (r *recipe) actionLabels(run string) map[string]string {
	return map[string]string{LabelStack: r.project, LabelAction: run}
}

func (r *recipe) actionMounts(run, box string, vols actionVolumes) []mountJSON {
	out := []mountJSON{{Type: "volume", Source: vols.Actions, Target: actionIn, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true, Subpath: run}}}
	if box != "" {
		out = append(out, mountJSON{Type: "volume", Source: vols.Workspace, Target: session.ActionWork, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true, Subpath: "s/" + box}})
	}
	if r.cfg.Live {
		out = append(out, mountJSON{Type: "volume", Source: vols.DevBin, Target: liveTarget, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true}})
	}
	return out
}

func (r *recipe) actionCreate(run, box string, subnet netip.Prefix, vols actionVolumes) containerCreateJSON {
	net := actionName(r.project, run)
	c := containerCreateJSON{
		Image:       r.image,
		User:        actionUser,
		Cmd:         []string{"action-run"},
		Env:         []string{"HOME=" + session.ActionHome, "LANG=C.UTF-8"},
		Labels:      r.blanked(r.actionLabels(run)),
		Healthcheck: healthJSON{Test: []string{"NONE"}},
		HostConfig: hostCreateJSON{
			Init:           true,
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			OomScoreAdj:    verifierOOMScore,
			Memory:         actionMemory,
			MemorySwap:     actionMemory,
			NanoCPUs:       actionCPUs,
			PidsLimit:      actionPids,
			RestartPolicy:  restartJSON{Name: "no"},
			LogConfig:      logJSON{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
			ExtraHosts:     []string{"egress:" + egressOf(subnet).String()},
			NetworkMode:    net,
			Mounts:         r.actionMounts(run, box, vols),
			Tmpfs:          map[string]string{session.ActionHome: actionHomeTmpfs, "/tmp": actionTmpTmpfs},
			GroupAdd:       []string{actionGroup},
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

func (r *recipe) actionGeneric(run, box string, subnet netip.Prefix) generic {
	pairs := []string{actionName(r.project, run), "{network}"}
	if box != "" {
		pairs = append(pairs, box, "{box}")
	}
	return genericOf(subnet, append(pairs, run, "{run}")...)
}

func ownedAction(project, name string, labels map[string]string) (string, bool) {
	run := labels[LabelAction]
	if labels[LabelStack] != project || !session.ValidBox(run) || name != actionName(project, run) {
		return "", false
	}
	for _, other := range []string{LabelBox, LabelReviewer, LabelVerifier} {
		if _, ok := labels[other]; ok {
			return "", false
		}
	}
	return run, true
}

func actionLabelled(project string, labels map[string]string) (string, bool) {
	run := labels[LabelAction]
	return run, labels[LabelStack] == project && session.ValidBox(run)
}

func (h *Helper) logAction(ctx context.Context, action, run, recipe string, err error) {
	fields := []any{"action", action, "run", run}
	if recipe != "" {
		fields = append(fields, "recipe", recipe)
	}
	if err == nil {
		shared.For(ctx, h.logger).Info("action container", append(fields, "outcome", "ok")...)
		return
	}
	reason, message := ReasonDocker, oneLine(err.Error())
	if r, ok := errors.AsType[*Refusal](err); ok {
		reason = r.Reason
	}
	shared.For(ctx, h.logger).Warn("action container", append(fields, "outcome", "refused", "reason", reason, "message", message)...)
}

func (h *Helper) actionVolumes(ctx context.Context, box string) (actionVolumes, error) {
	var v actionVolumes
	var err error
	if v.Actions, err = h.stackVolume(ctx, volumeActions); err != nil {
		return v, err
	}
	if box != "" {
		if v.Workspace, err = h.stackVolume(ctx, volumeWork); err != nil {
			return v, err
		}
	}
	if h.cfg.Live {
		if v.DevBin, err = h.stackVolume(ctx, volumeDevBin); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (h *Helper) MakeAction(ctx context.Context, run, box string) (session.ActionInfo, error) {
	if err := badAction(run, box); err != nil {
		return session.ActionInfo{}, err
	}
	unlock := h.locks.lock(actionLock(run))
	defer unlock()
	info, err := h.makeAction(ctx, run, box)
	h.logAction(ctx, "make", run, info.Recipe, err)
	return info, err
}

func (h *Helper) actionWant(run, box string, subnet netip.Prefix, vols actionVolumes) (containerCreateJSON, recipeView, generic) {
	body := h.rec.actionCreate(run, box, subnet, vols)
	g := h.rec.actionGeneric(run, box, subnet)
	return body, h.rec.viewOf(body, h.rec.wanted(h.rec.blanked(h.rec.actionLabels(run))), g, subnet), g
}

func (h *Helper) makeAction(ctx context.Context, run, box string) (session.ActionInfo, error) {
	vols, err := h.actionVolumes(ctx, box)
	if err != nil {
		return session.ActionInfo{}, err
	}
	if err := h.noActionStrays(ctx, vols.Actions); err != nil {
		return session.ActionInfo{}, err
	}
	if err := h.teardownAction(ctx, run); err != nil {
		return session.ActionInfo{}, err
	}
	if err := h.runtimeReady(ctx); err != nil {
		return session.ActionInfo{}, err
	}
	egress, err := h.service(ctx, egressService)
	if err != nil {
		return session.ActionInfo{}, err
	}
	subnet, release, err := h.reserveAction(ctx, run)
	if err != nil {
		return session.ActionInfo{}, err
	}
	defer release()
	name := actionName(h.rec.project, run)
	body, want, g := h.actionWant(run, box, subnet, vols)
	recipe := hashOf(want)
	info := session.ActionInfo{Run: run, Subnet: subnet, Recipe: recipe}
	n, err := h.makeNetworkFor(ctx, name, h.rec.actionLabels(run), subnet, func(n *networkInspectJSON) []string {
		return append(actionNetOwned(h.rec.project, run, n), shapeProblems(n, subnet)...)
	})
	if err != nil {
		return info, err
	}
	made := ""
	fail := func(e error) (session.ActionInfo, error) {
		r, ok := errors.AsType[*Refusal](e)
		if !ok {
			r = refuse(http.StatusBadGateway, ReasonDocker, "%s", oneLine(e.Error()))
		}
		out := *r
		out.Message += h.dropBuilt(ctx, made, n)
		return info, &out
	}
	if err := h.attach(ctx, n.ID, egress, egressOf(subnet)); err != nil {
		return fail(dockerFailed("connect egress to the action's network at "+egressOf(subnet).String(), err))
	}
	body.Labels[LabelRecipe] = recipe
	var created createdJSON
	if err := h.docker.post(ctx, "/containers/create", url.Values{"name": {name}}, body, &created); err != nil {
		var de *DockerError
		if errors.As(err, &de) && de.Code == http.StatusConflict {
			return fail(refuse(http.StatusConflict, ReasonNamed, "a container named %s already exists, so the helper makes no action container with that name", name))
		}
		return fail(actionFolderOr(dockerFailed("make the action container", err), err, run, box))
	}
	made = created.ID
	if len(created.Warnings) > 0 {
		return fail(refuse(http.StatusInternalServerError, ReasonRecipe, "Docker made the action container with warnings (%s)", oneLine(strings.Join(created.Warnings, "; "))))
	}
	if r := h.checkAction(ctx, created.ID, name, want, g, recipe, "before it started"); r != nil {
		return fail(r)
	}
	return info, nil
}

func actionNetOwned(project, run string, n *networkInspectJSON) []string {
	if got, ok := ownedAction(project, n.Name, n.Labels); !ok || got != run {
		return []string{"name or labels"}
	}
	return nil
}

func actionFolderOr(fallback *Refusal, err error, run, box string) *Refusal {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "subpath") || strings.Contains(msg, "no such file or directory") {
		return refuse(http.StatusConflict, ReasonFolders, "the run's folder %s in the actions volume, and s/%s in the workspace volume when it reads the session's files, must exist before its container starts (%s)", run, box, oneLine(err.Error()))
	}
	return fallback
}

func (h *Helper) checkAction(ctx context.Context, id, name string, want recipeView, g generic, recipe, when string) *Refusal {
	var got containerInspectJSON
	if err := h.docker.get(ctx, "/containers/"+pathPart(id)+"/json", nil, &got); err != nil {
		return dockerFailed("inspect the action container "+when, err)
	}
	diffs := differences(want, inspectedIn(got, g, name))
	if len(got.HostConfig.Ulimits) > 0 {
		diffs = append(diffs, "Ulimits")
	}
	if len(got.Mounts) != len(want.Mounts) {
		diffs = append(diffs, "MountCount")
	}
	if len(diffs) > 0 || got.Config.Labels[LabelRecipe] != recipe || got.Image != h.rec.image {
		return refuse(http.StatusInternalServerError, ReasonRecipe, "The action container as Docker made it %s differs from the recipe in %s", when, strings.Join(diffs, ", "))
	}
	return nil
}

func (h *Helper) StartAction(ctx context.Context, run string) error {
	if err := badAction(run, ""); err != nil {
		return err
	}
	unlock := h.locks.lock(actionLock(run))
	defer unlock()
	recipe, err := h.startAction(ctx, run)
	h.logAction(ctx, "start", run, recipe, err)
	return err
}

func (h *Helper) startAction(ctx context.Context, run string) (string, error) {
	ci, err := h.actionContainer(ctx, run)
	if err != nil {
		return "", err
	}
	if ci == nil {
		return "", refuse(http.StatusNotFound, ReasonUnknown, "the box helper has made no action container for the run %s", run)
	}
	if ci.State.Status != "created" {
		return "", refuse(http.StatusConflict, ReasonRecipe, "the action container of the run %s has already started once", run)
	}
	n, err := h.actionNetwork(ctx, run)
	if err != nil {
		return "", err
	}
	subnet, ok := boxSubnet(n)
	if n == nil || !ok {
		return "", refuse(http.StatusConflict, ReasonNetwork, "the action container of the run %s has no network of its own", run)
	}
	box := ""
	for _, m := range ci.HostConfig.Mounts {
		if m.Target == session.ActionWork && m.VolumeOptions != nil {
			box = strings.TrimPrefix(m.VolumeOptions.Subpath, "s/")
		}
	}
	vols, err := h.actionVolumes(ctx, box)
	if err != nil {
		return "", err
	}
	_, want, g := h.actionWant(run, box, subnet, vols)
	recipe := hashOf(want)
	name := actionName(h.rec.project, run)
	if r := h.checkAction(ctx, ci.ID, name, want, g, recipe, "before it started"); r != nil {
		return recipe, r
	}
	egress, err := h.service(ctx, egressService)
	if err != nil {
		return recipe, err
	}
	if problems := actionMembers(n, egress, ci.ID, subnet); len(problems) > 0 {
		return recipe, refuse(http.StatusInternalServerError, ReasonNetwork, "the action's network isn't as the helper made it (%s)", strings.Join(problems, ", "))
	}
	if err := h.docker.post(ctx, "/containers/"+pathPart(ci.ID)+"/start", nil, nil, nil); err != nil {
		return recipe, actionFolderOr(dockerFailed("start the action container", err), err, run, box)
	}
	if r := h.checkAction(ctx, ci.ID, name, want, g, recipe, "after it started"); r != nil {
		_ = h.docker.post(ctx, "/containers/"+pathPart(ci.ID)+"/kill", url.Values{"signal": {"KILL"}}, nil, nil)
		return recipe, r
	}
	return recipe, nil
}

func actionMembers(n *networkInspectJSON, egress, container string, subnet netip.Prefix) []string {
	var out []string
	want := map[string]netip.Addr{egress: egressOf(subnet), container: agentOf(subnet)}
	for id, addr := range want {
		if _, joined := n.Containers[id]; id == container && !joined {
			continue
		}
		if !connectedAt(n, id, addr) {
			out = append(out, "a member isn't at "+addr.String())
		}
	}
	for id, m := range n.Containers {
		if _, ok := want[id]; !ok {
			out = append(out, "another container "+oneLine(m.Name)+" is on it")
		}
	}
	slices.Sort(out)
	return out
}

func (h *Helper) actionContainer(ctx context.Context, run string) (*containerInspectJSON, error) {
	name := actionName(h.rec.project, run)
	var ci containerInspectJSON
	if err := h.docker.get(ctx, "/containers/"+pathPart(name)+"/json", nil, &ci); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, dockerFailed("inspect the container "+name, err)
	}
	if got, ok := ownedAction(h.rec.project, strings.TrimPrefix(ci.Name, "/"), ci.Config.Labels); !ok || got != run || !validRecipeLabel(ci.Config.Labels[LabelRecipe]) {
		return nil, refuse(http.StatusConflict, ReasonNamed, "a container named %s exists without the box helper's labels, so the helper leaves it alone", name)
	}
	return &ci, nil
}

func (h *Helper) actionNetwork(ctx context.Context, run string) (*networkInspectJSON, error) {
	name := actionName(h.rec.project, run)
	var n networkInspectJSON
	if err := h.docker.get(ctx, "/networks/"+pathPart(name), nil, &n); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, dockerFailed("inspect the network "+name, err)
	}
	if got, ok := ownedAction(h.rec.project, n.Name, n.Labels); !ok || got != run {
		return nil, refuse(http.StatusConflict, ReasonNamed, "a network named %s exists without the box helper's labels, so the helper leaves it alone", name)
	}
	return &n, nil
}

func (h *Helper) teardownAction(ctx context.Context, run string) error {
	ci, err := h.actionContainer(ctx, run)
	if err != nil {
		return err
	}
	if ci != nil {
		if err := h.removeVerifierContainer(ctx, ci.ID); err != nil {
			return err
		}
	}
	var list []containerListJSON
	q := filters(map[string][]string{"label": {LabelStack + "=" + h.rec.project, LabelAction + "=" + run}})
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": q["filters"]}, &list); err != nil {
		return dockerFailed("list the containers labelled as the action run "+run, err)
	}
	for _, c := range list {
		if got, ok := actionLabelled(h.rec.project, c.Labels); ok && got == run {
			if err := h.removeVerifierContainer(ctx, c.ID); err != nil {
				return err
			}
		}
	}
	n, err := h.actionNetwork(ctx, run)
	if err != nil || n == nil {
		return err
	}
	return h.dropNetwork(ctx, n)
}

func (h *Helper) reserveAction(ctx context.Context, run string) (netip.Prefix, func(), error) {
	h.global.Lock()
	defer h.global.Unlock()
	live, err := h.actionRuns(ctx, false)
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
		if strings.HasPrefix(k, actionLock("")) && k != actionLock(run) {
			n++
		}
	}
	if n >= maxActionRuns {
		return netip.Prefix{}, nil, refuse(http.StatusConflict, ReasonLimit, "%d action containers are running or starting, the most the box helper runs at once", n)
	}
	return h.pickLocked(ctx, actionLock(run), "action container")
}

func (h *Helper) actionRuns(ctx context.Context, all bool) (map[string]string, error) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": h.stackFilter()["filters"]}, &list); err != nil {
		return nil, dockerFailed("list the stack's action containers", err)
	}
	out := map[string]string{}
	for _, c := range list {
		run, ok := actionLabelled(h.rec.project, c.Labels)
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

func (h *Helper) noActionStrays(ctx context.Context, volume string) error {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", filters(map[string][]string{"volume": {volume}}), &list); err != nil {
		return dockerFailed("list the containers that mount the actions volume", err)
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
		if _, ok := ownedAction(h.rec.project, strings.TrimPrefix(name, "/"), c.Labels); ok {
			continue
		}
		found = append(found, oneLine(strings.TrimPrefix(name, "/")))
	}
	if len(found) == 0 {
		return nil
	}
	slices.Sort(found)
	if len(found) > maxNamed {
		found = found[:maxNamed]
	}
	return refuse(http.StatusConflict, ReasonShared, "other containers mount the stack's actions volume (%s), where every action run's credential files live, so the box helper makes no action container until they are gone (docker rm -f with its name)", strings.Join(found, ", "))
}

func (h *Helper) ActionState(ctx context.Context, run string) (session.ActionState, error) {
	if err := badAction(run, ""); err != nil {
		return session.ActionState{}, err
	}
	ci, err := h.actionContainer(ctx, run)
	if err != nil {
		return session.ActionState{}, err
	}
	if ci == nil {
		return session.ActionState{}, refuse(http.StatusNotFound, ReasonUnknown, "the box helper has no action container for the run %s", run)
	}
	st := session.ActionState{Run: run, State: oneLine(ci.State.Status), ExitCode: ci.State.ExitCode, OOMKilled: ci.State.OOMKilled, Error: oneLine(ci.State.Error),
		StartedAt: dockerTime(ci.State.StartedAt), FinishedAt: dockerTime(ci.State.FinishedAt)}
	if len(st.State) > verifierStateLength {
		st.State = st.State[:verifierStateLength]
	}
	return st, nil
}

func (h *Helper) ActionLog(ctx context.Context, run string) (string, bool, error) {
	if err := badAction(run, ""); err != nil {
		return "", false, err
	}
	ci, err := h.actionContainer(ctx, run)
	if err != nil {
		return "", false, err
	}
	if ci == nil {
		return "", false, refuse(http.StatusNotFound, ReasonUnknown, "the box helper has no action container for the run %s", run)
	}
	data, cut, err := h.docker.logTailOf(ctx, ci.ID, session.MaxActionOutput, verifierLogRead, "all")
	if err != nil {
		return "", false, dockerFailed("read the action container's output", err)
	}
	return strings.ToValidUTF8(string(data), "�"), cut, nil
}

func (h *Helper) RemoveAction(ctx context.Context, run string) error {
	if err := badAction(run, ""); err != nil {
		return err
	}
	unlock := h.locks.lock(actionLock(run))
	defer unlock()
	err := h.teardownAction(ctx, run)
	h.logAction(ctx, "remove", run, "", err)
	return err
}

func (h *Helper) ListActions(ctx context.Context) ([]session.BoxInfo, error) {
	runs, err := h.actionRuns(ctx, true)
	if err != nil {
		return nil, err
	}
	var nets []networkInspectJSON
	if err := h.docker.get(ctx, "/networks", h.stackFilter(), &nets); err != nil {
		return nil, dockerFailed("list the stack's networks", err)
	}
	for _, n := range nets {
		if run, ok := ownedAction(h.rec.project, n.Name, n.Labels); ok {
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

func (h *Helper) SweepActions(ctx context.Context, dry bool) {
	list, err := h.ListActions(ctx)
	if err != nil {
		h.logger.Warn("action sweep", "event", "list_failed", "error", oneLine(err.Error()))
		return
	}
	for _, r := range list {
		if dry {
			h.logger.Info("action sweep", "event", "would_remove", "run", r.Box, "state", r.State)
			continue
		}
		unlock := h.locks.lock(actionLock(r.Box))
		err := h.teardownAction(ctx, r.Box)
		unlock()
		h.logAction(ctx, "sweep", r.Box, "", err)
	}
}

func (h *Helper) killOverdueActions(ctx context.Context) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", h.stackFilter(), &list); err != nil {
		return
	}
	for _, c := range list {
		run, ok := actionLabelled(h.rec.project, c.Labels)
		if !ok || c.State != stateRunning {
			continue
		}
		var ci containerInspectJSON
		if err := h.docker.get(ctx, "/containers/"+pathPart(c.ID)+"/json", nil, &ci); err != nil {
			continue
		}
		started := dockerTime(ci.State.StartedAt)
		if !ci.State.Running || started.IsZero() || time.Since(started) <= actionKillAfter {
			continue
		}
		err := h.docker.post(ctx, "/containers/"+pathPart(c.ID)+"/kill", url.Values{"signal": {"KILL"}}, nil, nil)
		if err != nil && IsNotFound(err) {
			err = nil
		}
		h.logAction(ctx, "kill_overdue", run, ci.Config.Labels[LabelRecipe], err)
	}
}

func (h *Helper) reconnectActions(ctx context.Context, m stackMembers) {
	if m.egress == "" {
		return
	}
	runs, err := h.actionRuns(ctx, false)
	if err != nil {
		return
	}
	for run := range runs {
		unlock := h.locks.lock(actionLock(run))
		if n, err := h.actionNetwork(ctx, run); err == nil && n != nil {
			if subnet, ok := boxSubnet(n); ok {
				if err := h.ensure(ctx, n, m.egress, egressOf(subnet)); err != nil {
					h.logAction(ctx, "reconnect", run, "", err)
				}
			}
		}
		unlock()
	}
}
