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
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const cleanupFor = 90 * time.Second

func badBox(box string) error {
	if session.ValidBox(box) {
		return nil
	}
	return refuse(http.StatusBadRequest, ReasonInput, "a box is 16 lowercase hexadecimal digits")
}

func (h *Helper) logResult(action, box, recipe string, err error) {
	fields := []any{"action", action, "box", box}
	if recipe != "" {
		fields = append(fields, "recipe", recipe)
	}
	if err == nil {
		h.logger.Info("box", append(fields, "outcome", "ok")...)
		return
	}
	reason, message := ReasonDocker, oneLine(err.Error())
	if r, ok := errors.AsType[*Refusal](err); ok {
		reason = r.Reason
	}
	h.logger.Warn("box", append(fields, "outcome", "refused", "reason", reason, "message", message)...)
}

func (h *Helper) Create(ctx context.Context, box string) error {
	if err := badBox(box); err != nil {
		return err
	}
	unlock := h.locks.lock(box)
	defer unlock()
	err := h.create(ctx, box)
	h.logResult("create", box, "", err)
	return err
}

func (h *Helper) create(ctx context.Context, box string) error {
	found, err := h.home(ctx, box)
	if err != nil || found {
		return err
	}
	name := homeName(h.rec.project, box)
	var v volumeJSON
	if err := h.docker.post(ctx, "/volumes/create", nil, volumeCreateJSON{Name: name, Driver: "local", Labels: h.rec.labels(box)}, &v); err != nil {
		return dockerFailed("make the volume "+name, err)
	}
	if got, ok := ownedName(h.rec.project, homePrefix, v.Name, v.Labels); !ok || got != box {
		return refuse(http.StatusConflict, ReasonNamed, "a volume named %s exists without the box helper's labels, so the helper leaves it alone and makes no box with that name", name)
	}
	return nil
}

func (h *Helper) Start(ctx context.Context, box string) (session.BoxInfo, error) {
	if err := badBox(box); err != nil {
		return session.BoxInfo{}, err
	}
	unlock := h.locks.lock(box)
	defer unlock()
	info, err := h.start(ctx, box)
	h.logResult("start", box, info.Recipe, err)
	return info, err
}

func (h *Helper) start(ctx context.Context, box string) (session.BoxInfo, error) {
	found, err := h.home(ctx, box)
	if err != nil {
		return session.BoxInfo{}, err
	}
	if !found {
		return session.BoxInfo{}, refuse(http.StatusNotFound, ReasonUnknown, "the box helper has made no box %s; create it first", box)
	}
	vols, err := h.stackVolumes(ctx)
	if err != nil {
		return session.BoxInfo{}, err
	}
	ci, err := h.boxContainer(ctx, box)
	if err != nil {
		return session.BoxInfo{}, err
	}
	n, err := h.boxNetwork(ctx, box)
	if err != nil {
		return session.BoxInfo{}, err
	}
	if ci != nil && n != nil {
		if info, ok := h.reuse(ctx, box, ci, n, vols); ok {
			return info, nil
		}
	}
	if _, err := h.teardown(ctx, box); err != nil {
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
	subnet, release, err := h.reserve(ctx, box)
	if err != nil {
		return session.BoxInfo{}, err
	}
	defer release()
	return h.build(ctx, box, subnet, vols, engine, egress)
}

func (h *Helper) build(ctx context.Context, box string, subnet netip.Prefix, vols stackVolumes, engine, egress string) (session.BoxInfo, error) {
	want := h.rec.expectedView(box, subnet, vols)
	recipe := hashOf(want)
	n, err := h.makeNetwork(ctx, box, subnet)
	if err != nil {
		return session.BoxInfo{Box: box, Recipe: recipe}, err
	}
	made := ""
	fail := func(e error) (session.BoxInfo, error) {
		r, ok := errors.AsType[*Refusal](e)
		if !ok {
			r = refuse(http.StatusBadGateway, ReasonDocker, "%s", oneLine(e.Error()))
		}
		out := *r
		out.Message += h.dropBuilt(ctx, made, n)
		return session.BoxInfo{Box: box, Recipe: recipe}, &out
	}
	if err := h.attach(ctx, n.ID, engine, engineOf(subnet)); err != nil {
		return fail(dockerFailed("connect the engine to the box's network at "+engineOf(subnet).String(), err))
	}
	if err := h.attach(ctx, n.ID, egress, egressOf(subnet)); err != nil {
		return fail(dockerFailed("connect egress to the box's network at "+egressOf(subnet).String(), err))
	}
	body := h.rec.create(box, subnet, vols)
	body.Labels[LabelRecipe] = recipe
	name := containerName(h.rec.project, box)
	var created createdJSON
	if err := h.docker.post(ctx, "/containers/create", url.Values{"name": {name}}, body, &created); err != nil {
		var de *DockerError
		if errors.As(err, &de) && de.Code == http.StatusConflict {
			return fail(refuse(http.StatusConflict, ReasonNamed, "a container named %s already exists, so the helper makes no box with that name", name))
		}
		return fail(folderOr(dockerFailed("make the box's container", err), err, box))
	}
	made = created.ID
	if len(created.Warnings) > 0 {
		return fail(refuse(http.StatusInternalServerError, ReasonRecipe, "Docker made the box's container with warnings (%s)", oneLine(strings.Join(created.Warnings, "; "))))
	}
	var got containerInspectJSON
	if err := h.docker.get(ctx, "/containers/"+pathPart(created.ID)+"/json", nil, &got); err != nil {
		return fail(dockerFailed("inspect the box's container", err))
	}
	if diffs := differences(want, h.rec.inspectedView(got, box, subnet)); len(diffs) > 0 || got.Config.Labels[LabelRecipe] != recipe {
		return fail(refuse(http.StatusInternalServerError, ReasonRecipe, "the box's container as Docker made it differs from the recipe in %s", strings.Join(diffs, ", ")))
	}
	if err := h.docker.post(ctx, "/containers/"+pathPart(created.ID)+"/start", nil, nil, nil); err != nil {
		return fail(folderOr(dockerFailed("start the box's container", err), err, box))
	}
	if _, err := h.waitHealthy(ctx, created.ID); err != nil {
		return fail(err)
	}
	var ready containerInspectJSON
	if err := h.docker.get(ctx, "/containers/"+pathPart(created.ID)+"/json", nil, &ready); err != nil {
		return fail(dockerFailed("inspect the box's container after it started", err))
	}
	if diffs := differences(want, h.rec.inspectedView(ready, box, subnet)); len(diffs) > 0 || ready.Config.Labels[LabelRecipe] != recipe {
		return fail(refuse(http.StatusInternalServerError, ReasonRecipe, "the box's container changed while it started and now differs from the recipe in %s", strings.Join(diffs, ", ")))
	}
	fresh, err := h.boxNetwork(ctx, box)
	if err != nil || fresh == nil || fresh.ID != n.ID {
		return fail(refuse(http.StatusInternalServerError, ReasonNetwork, "the box's network changed or went away while the box started"))
	}
	n = fresh
	if problems := append(networkProblems(n, h.rec.project, box, subnet), memberProblems(n, engine, egress, created.ID, subnet)...); len(problems) > 0 {
		return fail(refuse(http.StatusInternalServerError, ReasonNetwork, "the box's network changed while the box started (%s)", strings.Join(problems, ", ")))
	}
	return h.infoOf(ready, n, box, &vols, h.membersOf(ctx)), nil
}

func memberProblems(n *networkInspectJSON, engine, egress, box string, subnet netip.Prefix) []string {
	var out []string
	want := map[string]netip.Addr{engine: engineOf(subnet), egress: egressOf(subnet), box: agentOf(subnet)}
	for id, addr := range want {
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

func (h *Helper) dropBuilt(ctx context.Context, container string, n *networkInspectJSON) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupFor)
	defer cancel()
	var problems []string
	if container != "" {
		if err := h.dropContainer(ctx, container); err != nil {
			problems = append(problems, "the container: "+oneLine(err.Error()))
		}
	}
	if err := h.dropNetwork(ctx, n); err != nil {
		problems = append(problems, "the network: "+oneLine(err.Error()))
	}
	if len(problems) > 0 {
		h.logger.Warn("box", "action", "cleanup", "outcome", "refused", "message", strings.Join(problems, "; "))
		return ", and the helper couldn't remove what it made (" + strings.Join(problems, "; ") + ")"
	}
	if container != "" {
		return ", so the helper removed the container and network it made"
	}
	return ", so the helper removed the network it made"
}

func folderOr(fallback *Refusal, err error, box string) error {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "subpath") || strings.Contains(msg, "no such file or directory") {
		return refuse(http.StatusConflict, ReasonFolders, "the box's folders must exist before it starts: s/%s in the workspace volume and %s in the uploads volume, which the engine makes (%s)", box, box, oneLine(err.Error()))
	}
	return fallback
}

func (h *Helper) runtimeReady(ctx context.Context) error {
	if h.cfg.Runtime == "" {
		return nil
	}
	var info infoJSON
	if err := h.docker.get(ctx, "/info", nil, &info); err != nil {
		return dockerFailed("read its system information", err)
	}
	if _, ok := info.Runtimes[h.cfg.Runtime]; !ok {
		return refuse(http.StatusConflict, ReasonRuntime, "SF_BOX_RUNTIME=%s asks for gVisor, and Docker lists no %s runtime, so the helper starts no box rather than run one without it", h.cfg.Runtime, h.cfg.Runtime)
	}
	return nil
}

func (h *Helper) reuse(ctx context.Context, box string, ci *containerInspectJSON, n *networkInspectJSON, vols stackVolumes) (session.BoxInfo, bool) {
	subnet, ok := boxSubnet(n)
	if !ok || !ci.State.Running || ci.State.Health == nil || ci.State.Health.Status != healthHealthy || ci.Image != h.rec.image {
		return session.BoxInfo{}, false
	}
	want := h.rec.expectedView(box, subnet, vols)
	if len(differences(want, h.rec.inspectedView(*ci, box, subnet))) > 0 || ci.Config.Labels[LabelRecipe] != hashOf(want) {
		return session.BoxInfo{}, false
	}
	if len(networkProblems(n, h.rec.project, box, subnet)) > 0 {
		return session.BoxInfo{}, false
	}
	if h.cfg.Runtime != "" && h.runtimeReady(ctx) != nil {
		return session.BoxInfo{}, false
	}
	m := h.membersOf(ctx)
	if m.engine == "" || m.egress == "" {
		return session.BoxInfo{}, false
	}
	if err := h.ensure(ctx, n, m.engine, engineOf(subnet)); err != nil {
		return session.BoxInfo{}, false
	}
	if err := h.ensure(ctx, n, m.egress, egressOf(subnet)); err != nil {
		return session.BoxInfo{}, false
	}
	fresh, err := h.boxNetwork(ctx, box)
	if err != nil || fresh == nil {
		return session.BoxInfo{}, false
	}
	return h.infoOf(*ci, fresh, box, &vols, m), true
}

func (h *Helper) Stop(ctx context.Context, box string) error {
	if err := badBox(box); err != nil {
		return err
	}
	unlock := h.locks.lock(box)
	defer unlock()
	recipe, err := h.teardown(ctx, box)
	h.logResult("stop", box, recipe, err)
	return err
}

func (h *Helper) Remove(ctx context.Context, box string) error {
	if err := badBox(box); err != nil {
		return err
	}
	unlock := h.locks.lock(box)
	defer unlock()
	recipe, err := h.remove(ctx, box)
	h.logResult("remove", box, recipe, err)
	return err
}

func (h *Helper) remove(ctx context.Context, box string) (string, error) {
	recipe, err := h.teardown(ctx, box)
	if err != nil {
		return recipe, err
	}
	found, err := h.home(ctx, box)
	if err != nil || !found {
		return recipe, err
	}
	name := homeName(h.rec.project, box)
	if err := h.docker.delete(ctx, "/volumes/"+pathPart(name), nil); err != nil && !IsNotFound(err) {
		return recipe, dockerFailed("remove the volume "+name, err)
	}
	return recipe, nil
}

type stackMembers struct{ engine, egress string }

func (h *Helper) membersOf(ctx context.Context) stackMembers {
	var m stackMembers
	m.engine, _ = h.service(ctx, engineService)
	m.egress, _ = h.service(ctx, egressService)
	return m
}

func (h *Helper) List(ctx context.Context) ([]session.BoxInfo, error) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": h.stackFilter()["filters"]}, &list); err != nil {
		return nil, dockerFailed("list the stack's boxes", err)
	}
	var vols *stackVolumes
	if v, err := h.stackVolumes(ctx); err == nil {
		vols = &v
	}
	m := h.membersOf(ctx)
	seen := map[string]bool{}
	var out []session.BoxInfo
	for _, c := range list {
		if len(c.Names) == 0 {
			continue
		}
		box, ok := h.ownsContainer(ctx, c.Names[0], c.ImageID, c.Labels)
		if !ok || seen[box] {
			continue
		}
		var ci containerInspectJSON
		if err := h.docker.get(ctx, "/containers/"+pathPart(c.ID)+"/json", nil, &ci); err != nil {
			if IsNotFound(err) {
				continue
			}
			return nil, dockerFailed("inspect the box "+box, err)
		}
		n, err := h.boxNetwork(ctx, box)
		if err != nil {
			n = nil
		}
		seen[box] = true
		out = append(out, h.infoOf(ci, n, box, vols, m))
	}
	var vl volumeListJSON
	if err := h.docker.get(ctx, "/volumes", h.stackFilter(), &vl); err != nil {
		return nil, dockerFailed("list the stack's volumes", err)
	}
	for _, v := range vl.Volumes {
		box, ok := ownedName(h.rec.project, homePrefix, v.Name, v.Labels)
		if !ok || seen[box] {
			continue
		}
		seen[box] = true
		out = append(out, session.BoxInfo{Box: box, State: "stopped"})
	}
	slices.SortFunc(out, func(a, b session.BoxInfo) int { return strings.Compare(a.Box, b.Box) })
	return out, nil
}

func stateOf(ci containerInspectJSON) string {
	if !ci.State.Running {
		return ci.State.Status
	}
	if ci.State.Health == nil {
		return stateRunning
	}
	switch ci.State.Health.Status {
	case healthHealthy:
		return stateRunning
	case "starting":
		return "starting"
	}
	return "unhealthy"
}

func (h *Helper) infoOf(ci containerInspectJSON, n *networkInspectJSON, box string, vols *stackVolumes, m stackMembers) session.BoxInfo {
	info := session.BoxInfo{Box: box, State: stateOf(ci)}
	subnet, ok := boxSubnet(n)
	got := h.rec.inspectedView(ci, box, subnet)
	info.Recipe = hashOf(got)
	verified := "differs"
	if vols != nil && ok && info.Recipe == hashOf(h.rec.expectedView(box, subnet, *vols)) && ci.Config.Labels[LabelRecipe] == info.Recipe && ci.Image == h.rec.image {
		verified = "verified"
	} else if vols == nil {
		verified = "unknown"
	}
	var names []string
	for name := range ci.NetworkSettings.Networks {
		names = append(names, name)
	}
	slices.Sort(names)
	own := networkName(h.rec.project, box)
	address := ""
	if ep, found := ci.NetworkSettings.Networks[own]; found && ep.IPAMConfig != nil {
		address = ep.IPAMConfig.IPv4Address
	}
	h0 := ci.HostConfig
	facts := []string{
		"recipe=" + verified,
		"image=" + ci.Image,
		"networks=" + strings.Join(names, ","),
		"own-network-only=" + strconv.FormatBool(len(names) == 1 && names[0] == own),
		"address=" + address,
		"runtime=" + h0.Runtime,
		"readonly-root=" + strconv.FormatBool(h0.ReadonlyRootfs),
		"cap-add=" + listOrNone(h0.CapAdd),
		"cap-drop=" + listOrNone(h0.CapDrop),
		"privileged=" + strconv.FormatBool(h0.Privileged),
		"security-opt=" + listOrNone(h0.SecurityOpt),
	}
	if ci.State.Health != nil {
		facts = append(facts, "health="+ci.State.Health.Status)
	}
	if ok {
		info.Subnet = subnet
		info.Gateway = gatewayOf(subnet)
		if len(n.IPAM.Config) == 1 {
			if g, err := netip.ParseAddr(n.IPAM.Config[0].Gateway); err == nil {
				info.Gateway = g
			}
		}
		info.Engine, info.Agent = engineOf(subnet), agentOf(subnet)
		info.Host = net.JoinHostPort(agentOf(subnet).String(), agentHostPort)
		facts = append(facts,
			"network-internal="+strconv.FormatBool(n.Internal),
			"network-ipv6="+strconv.FormatBool(n.EnableIPv6),
			"gateway-mode="+n.Options[gatewayModeKey],
			"engine-connected="+strconv.FormatBool(connectedAt(n, m.engine, engineOf(subnet))),
			"egress-connected="+strconv.FormatBool(connectedAt(n, m.egress, egressOf(subnet))),
		)
	} else {
		facts = append(facts, "network=missing")
	}
	info.Facts = facts
	return info
}

func listOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ",")
}

func connectedAt(n *networkInspectJSON, id string, addr netip.Addr) bool {
	if n == nil || id == "" {
		return false
	}
	m, ok := n.Containers[id]
	if !ok {
		return false
	}
	p, err := netip.ParsePrefix(m.IPv4Address)
	return err == nil && p.Addr() == addr
}

func (h *Helper) ensure(ctx context.Context, n *networkInspectJSON, id string, addr netip.Addr) error {
	if connectedAt(n, id, addr) {
		return nil
	}
	if _, ok := n.Containers[id]; ok {
		h.detach(ctx, n.ID, id, true)
	}
	if err := h.attach(ctx, n.ID, id, addr); err != nil {
		return dockerFailed("connect a container to "+n.Name+" at "+addr.String(), err)
	}
	h.logger.Info("box", "action", "reconnect", "network", n.Name, "container", id, "address", addr.String(), "outcome", "ok")
	return nil
}
