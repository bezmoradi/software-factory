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
)

const (
	ReasonVolume    = "volume"
	ReasonShared    = "shared"
	labelConfigHash = "com.docker.compose.config-hash"
	labelNumber     = "com.docker.compose.container-number"
	uploadsOptions  = "size=320m,uid=10001,gid=10000,mode=0710"
	maxNamed        = 5
)

func (h *Helper) projectFilter(extra ...string) url.Values {
	return filters(map[string][]string{"label": append([]string{labelProject + "=" + h.rec.project}, extra...)})
}

func (h *Helper) stackFilter() url.Values {
	return filters(map[string][]string{"label": {LabelStack + "=" + h.rec.project}})
}

func (h *Helper) service(ctx context.Context, name string) (string, error) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", h.projectFilter(labelService+"="+name), &list); err != nil {
		return "", dockerFailed("list the stack's "+name+" container", err)
	}
	var ids []string
	for _, c := range list {
		if !composeMade(c.Labels, h.rec.project, name) || c.State != stateRunning {
			continue
		}
		ids = append(ids, c.ID)
	}
	switch len(ids) {
	case 0:
		return "", refuse(http.StatusConflict, ReasonService, "the stack's %s container isn't running", name)
	case 1:
		return ids[0], nil
	}
	return "", refuse(http.StatusConflict, ReasonService, "%d containers run as the stack's %s service, and the box helper connects only one", len(ids), name)
}

func composeMade(labels map[string]string, project, service string) bool {
	if labels[labelProject] != project || labels[labelService] != service || labels[labelOneOff] == oneOffTrue {
		return false
	}
	if labels[labelConfigHash] == "" || labels[labelNumber] == "" {
		return false
	}
	_, boxed := labels[LabelBox]
	return !boxed
}

func (h *Helper) members(ctx context.Context) []string {
	var out []string
	for _, name := range []string{engineService, egressService} {
		var list []containerListJSON
		if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": h.projectFilter(labelService + "=" + name)["filters"]}, &list); err != nil {
			continue
		}
		for _, c := range list {
			if composeMade(c.Labels, h.rec.project, name) {
				out = append(out, c.ID)
			}
		}
	}
	return out
}

func (h *Helper) stackVolume(ctx context.Context, name string) (string, error) {
	var list volumeListJSON
	if err := h.docker.get(ctx, "/volumes", h.projectFilter(labelVolume+"="+name), &list); err != nil {
		return "", dockerFailed("list the stack's volumes", err)
	}
	var found []volumeJSON
	for _, v := range list.Volumes {
		if v.Labels[labelProject] == h.rec.project && v.Labels[labelVolume] == name {
			found = append(found, v)
		}
	}
	switch len(found) {
	case 0:
		return "", refuse(http.StatusConflict, ReasonVolume, "the stack has no %s volume", name)
	case 1:
		if !volumeShaped(name, found[0]) {
			return "", refuse(http.StatusConflict, ReasonVolume, "the stack's %s volume %s isn't made the way compose.yaml makes it (driver %s, options %s), so the box helper mounts it in no session", name, found[0].Name, oneLine(found[0].Driver), optionsOf(found[0].Options))
		}
		return found[0].Name, nil
	}
	return "", refuse(http.StatusConflict, ReasonVolume, "the stack has %d volumes labelled %s, and the box helper uses only one", len(found), name)
}

func volumeShaped(name string, v volumeJSON) bool {
	if v.Driver != volumeDriver {
		return false
	}
	if name != volumeUploads {
		return len(v.Options) == 0
	}
	return len(v.Options) == 3 && v.Options["type"] == "tmpfs" && v.Options["device"] == "tmpfs" && v.Options["o"] == uploadsOptions
}

func optionsOf(opts map[string]string) string {
	if len(opts) == 0 {
		return "none"
	}
	var out []string
	for k, v := range opts {
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return oneLine(strings.Join(out, " "))
}

func (h *Helper) strays(ctx context.Context, workspace string) ([]string, error) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", filters(map[string][]string{"volume": {workspace}}), &list); err != nil {
		return nil, dockerFailed("list the containers that mount the workspace volume", err)
	}
	var out []string
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = c.Names[0]
		}
		if composeMade(c.Labels, h.rec.project, engineService) {
			continue
		}
		if _, ok := h.ownsContainer(ctx, name, c.ImageID, c.Labels); ok {
			continue
		}
		if _, renamed := renamedBox(h.rec.project, c); renamed {
			continue
		}
		out = append(out, oneLine(strings.TrimPrefix(name, "/")))
	}
	slices.Sort(out)
	return out, nil
}

func (h *Helper) noStrays(ctx context.Context, workspace string) error {
	found, err := h.strays(ctx, workspace)
	if err != nil || len(found) == 0 {
		return err
	}
	shown := found
	if len(shown) > maxNamed {
		shown = shown[:maxNamed]
	}
	return refuse(http.StatusConflict, ReasonShared, "%d other containers mount the stack's workspace volume (%s), where every session's clone lives, so the box helper starts no session until they are gone: remove them, such as the shared agent container an earlier version ran (docker compose up -d --remove-orphans, or docker rm -f with its name)", len(found), strings.Join(shown, ", "))
}

func (h *Helper) twins(ctx context.Context, box string) ([]containerListJSON, error) {
	var list []containerListJSON
	q := filters(map[string][]string{"label": {LabelStack + "=" + h.rec.project, LabelBox + "=" + box}})
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": q["filters"]}, &list); err != nil {
		return nil, dockerFailed("list the containers labelled as the box "+box, err)
	}
	want := "/" + containerName(h.rec.project, box)
	var out []containerListJSON
	for _, c := range list {
		if c.Labels[LabelStack] != h.rec.project || c.Labels[LabelBox] != box {
			continue
		}
		if len(c.Names) == 1 && c.Names[0] == want {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (h *Helper) dropTwins(ctx context.Context, box string) error {
	list, err := h.twins(ctx, box)
	if err != nil {
		return err
	}
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = oneLine(c.Names[0])
		}
		if err := h.dropContainer(ctx, c.ID); err != nil {
			return err
		}
		h.logger.Warn("box", "action", "remove_renamed", "box", box, "name", name, "outcome", "ok")
	}
	return nil
}

func renamedBox(project string, c containerListJSON) (string, bool) {
	box := c.Labels[LabelBox]
	if c.Labels[LabelStack] != project || !session.ValidBox(box) {
		return "", false
	}
	return box, len(c.Names) != 1 || c.Names[0] != "/"+containerName(project, box)
}

func (h *Helper) stackVolumes(ctx context.Context) (stackVolumes, error) {
	var v stackVolumes
	var err error
	if v.Workspace, err = h.stackVolume(ctx, volumeWork); err != nil {
		return v, err
	}
	if v.Agents, err = h.stackVolume(ctx, volumeAgents); err != nil {
		return v, err
	}
	if v.Uploads, err = h.stackVolume(ctx, volumeUploads); err != nil {
		return v, err
	}
	if h.cfg.Live {
		if v.DevBin, err = h.stackVolume(ctx, volumeDevBin); err != nil {
			return v, err
		}
	}
	return v, nil
}

func (h *Helper) home(ctx context.Context, box string) (bool, error) {
	name := homeName(h.rec.project, box)
	var v volumeJSON
	if err := h.docker.get(ctx, "/volumes/"+pathPart(name), nil, &v); err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, dockerFailed("inspect the volume "+name, err)
	}
	if got, ok := ownedName(h.rec.project, homePrefix, v.Name, v.Labels); !ok || got != box {
		return false, refuse(http.StatusConflict, ReasonNamed, "a volume named %s exists without the box helper's labels, so the helper leaves it alone and makes no box with that name", name)
	}
	return true, nil
}

func (h *Helper) ownsContainer(ctx context.Context, name, image string, labels map[string]string) (string, bool) {
	box, ok := ownedName(h.rec.project, boxPrefix, strings.TrimPrefix(name, "/"), labels)
	if !ok || !validRecipeLabel(labels[LabelRecipe]) {
		return "", false
	}
	if composeBlank(labels) {
		return box, true
	}
	il, ok := h.imageLabels(ctx, image)
	if !ok || !composeMatches(labels, il) {
		return "", false
	}
	return box, true
}

func (h *Helper) imageLabels(ctx context.Context, image string) (map[string]string, bool) {
	if !strings.HasPrefix(image, "sha256:") {
		return nil, false
	}
	h.images.Lock()
	il, ok := h.labels[image]
	h.images.Unlock()
	if ok {
		return il, true
	}
	var img imageInspectJSON
	if err := h.docker.get(ctx, "/images/"+pathPart(image)+"/json", nil, &img); err != nil || img.ID != image {
		return nil, false
	}
	h.images.Lock()
	if len(h.labels) > 64 {
		clear(h.labels)
	}
	h.labels[image] = img.Config.Labels
	h.images.Unlock()
	return img.Config.Labels, true
}

func (h *Helper) boxContainer(ctx context.Context, box string) (*containerInspectJSON, error) {
	name := containerName(h.rec.project, box)
	var ci containerInspectJSON
	if err := h.docker.get(ctx, "/containers/"+pathPart(name)+"/json", nil, &ci); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, dockerFailed("inspect the container "+name, err)
	}
	if got, ok := h.ownsContainer(ctx, ci.Name, ci.Image, ci.Config.Labels); !ok || got != box {
		return nil, refuse(http.StatusConflict, ReasonNamed, "a container named %s exists without the box helper's labels, so the helper leaves it alone and makes no box with that name", name)
	}
	return &ci, nil
}

func (h *Helper) boxNetwork(ctx context.Context, box string) (*networkInspectJSON, error) {
	name := networkName(h.rec.project, box)
	var n networkInspectJSON
	if err := h.docker.get(ctx, "/networks/"+pathPart(name), nil, &n); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, dockerFailed("inspect the network "+name, err)
	}
	if got, ok := ownedName(h.rec.project, boxPrefix, n.Name, n.Labels); !ok || got != box {
		return nil, refuse(http.StatusConflict, ReasonNamed, "a network named %s exists without the box helper's labels, so the helper leaves it alone and makes no box with that name", name)
	}
	return &n, nil
}

func (h *Helper) dropContainer(ctx context.Context, id string) error {
	if err := h.docker.post(ctx, "/containers/"+pathPart(id)+"/stop", url.Values{"t": {"10"}}, nil, nil); err != nil && !IsNotFound(err) {
		h.logger.Warn("box helper", "event", "stop_failed", "container", id, "error", oneLine(err.Error()))
	}
	if err := h.docker.delete(ctx, "/containers/"+pathPart(id), url.Values{"force": {"true"}}); err != nil && !IsNotFound(err) {
		return dockerFailed("remove the box's container", err)
	}
	return nil
}

func (h *Helper) dropNetwork(ctx context.Context, n *networkInspectJSON) error {
	seen := map[string]bool{}
	for id := range n.Containers {
		seen[id] = true
		h.detach(ctx, n.ID, id, true)
	}
	for _, id := range h.members(ctx) {
		if !seen[id] {
			h.detach(ctx, n.ID, id, false)
		}
	}
	if err := h.docker.delete(ctx, "/networks/"+pathPart(n.ID), nil); err != nil && !IsNotFound(err) {
		return dockerFailed("remove the box's network "+n.Name, err)
	}
	return nil
}

func (h *Helper) detach(ctx context.Context, network, container string, report bool) {
	err := h.docker.post(ctx, "/networks/"+pathPart(network)+"/disconnect", nil, networkDisconnectJSON{Container: container, Force: true}, nil)
	if err != nil && report && !IsNotFound(err) {
		h.logger.Warn("box helper", "event", "disconnect_failed", "network", network, "container", container, "error", oneLine(err.Error()))
	}
}

func (h *Helper) attach(ctx context.Context, network, container string, addr netip.Addr) error {
	body := networkConnectJSON{Container: container, EndpointConfig: endpointCreateJSON{IPAMConfig: ipamAddressJSON{IPv4Address: addr.String()}}}
	return h.docker.post(ctx, "/networks/"+pathPart(network)+"/connect", nil, body, nil)
}

func (h *Helper) teardown(ctx context.Context, box string) (string, error) {
	ci, err := h.boxContainer(ctx, box)
	if err != nil {
		return "", err
	}
	recipe := ""
	if ci != nil {
		recipe = ci.Config.Labels[LabelRecipe]
		if err := h.dropContainer(ctx, ci.ID); err != nil {
			return recipe, err
		}
	}
	if err := h.dropTwins(ctx, box); err != nil {
		return recipe, err
	}
	n, err := h.boxNetwork(ctx, box)
	if err != nil {
		return recipe, err
	}
	if n != nil {
		return recipe, h.dropNetwork(ctx, n)
	}
	return recipe, nil
}

func (h *Helper) makeNetwork(ctx context.Context, box string, subnet netip.Prefix) (*networkInspectJSON, error) {
	name := networkName(h.rec.project, box)
	body := networkCreateJSON{
		Name: name, Driver: "bridge", Internal: true, EnableIPv6: false,
		IPAM:    ipamJSON{Driver: "default", Config: []ipamConfigJSON{{Subnet: subnet.String()}}},
		Options: map[string]string{gatewayModeKey: gatewayIsolate},
		Labels:  h.rec.labels(box),
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := h.docker.post(ctx, "/networks/create", nil, body, &created); err != nil {
		var de *DockerError
		if errors.As(err, &de) && de.Code == http.StatusConflict {
			return nil, refuse(http.StatusConflict, ReasonNamed, "a network named %s already exists, so the helper makes no box with that name", name)
		}
		return nil, dockerFailed("make the box's network "+name+" on "+subnet.String(), err)
	}
	var n networkInspectJSON
	if err := h.docker.get(ctx, "/networks/"+pathPart(created.ID), nil, &n); err != nil {
		r := dockerFailed("inspect the box's network "+name, err)
		r.Message += h.dropMade(ctx, created.ID, nil)
		return nil, r
	}
	if problems := networkProblems(&n, h.rec.project, box, subnet); len(problems) > 0 {
		r := refuse(http.StatusInternalServerError, ReasonNetwork, "Docker made the box's network %s differently from the recipe (%s)", name, strings.Join(problems, ", "))
		r.Message += h.dropMade(ctx, created.ID, &n)
		return nil, r
	}
	return &n, nil
}

func (h *Helper) dropMade(ctx context.Context, network string, n *networkInspectJSON) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupFor)
	defer cancel()
	var err error
	if n != nil {
		err = h.dropNetwork(ctx, n)
	} else if derr := h.docker.delete(ctx, "/networks/"+pathPart(network), nil); derr != nil && !IsNotFound(derr) {
		err = derr
	}
	if err != nil {
		return ", and the helper couldn't remove the network it made: " + oneLine(err.Error())
	}
	return ", so the helper removed the network it made"
}

func networkProblems(n *networkInspectJSON, project, box string, subnet netip.Prefix) []string {
	var out []string
	if got, ok := ownedName(project, boxPrefix, n.Name, n.Labels); !ok || got != box {
		out = append(out, "name or labels")
	}
	if n.Driver != "bridge" {
		out = append(out, "driver "+n.Driver)
	}
	if !n.Internal {
		out = append(out, "not internal")
	}
	if n.EnableIPv6 {
		out = append(out, "IPv6 on")
	}
	if n.Options[gatewayModeKey] != gatewayIsolate {
		out = append(out, "gateway mode "+n.Options[gatewayModeKey])
	}
	if s, ok := boxSubnet(n); !ok || s != subnet {
		out = append(out, "subnet")
	}
	return out
}

func (h *Helper) usedSubnets(ctx context.Context) ([]netip.Prefix, error) {
	var nets []networkInspectJSON
	if err := h.docker.get(ctx, "/networks", nil, &nets); err != nil {
		return nil, dockerFailed("list the networks", err)
	}
	var out []netip.Prefix
	for _, n := range nets {
		out = append(out, subnetsOf(n)...)
	}
	return out, nil
}

func (h *Helper) liveBoxes(ctx context.Context) (map[string]bool, error) {
	var list []containerListJSON
	if err := h.docker.get(ctx, "/containers/json", url.Values{"all": {"true"}, "filters": h.stackFilter()["filters"]}, &list); err != nil {
		return nil, dockerFailed("list the stack's boxes", err)
	}
	out := map[string]bool{}
	for _, c := range list {
		if len(c.Names) == 0 {
			continue
		}
		box, ok := h.ownsContainer(ctx, c.Names[0], c.ImageID, c.Labels)
		if !ok {
			if box, ok = renamedBox(h.rec.project, c); !ok {
				continue
			}
		}
		switch c.State {
		case "exited", "dead", "removing":
		default:
			out[box] = true
		}
	}
	return out, nil
}

func (h *Helper) reserve(ctx context.Context, box string) (netip.Prefix, func(), error) {
	h.global.Lock()
	defer h.global.Unlock()
	if h.cfg.MaxBoxes > 0 {
		live, err := h.liveBoxes(ctx)
		if err != nil {
			return netip.Prefix{}, nil, err
		}
		for b := range h.pending {
			live[b] = true
		}
		delete(live, box)
		if len(live) >= h.cfg.MaxBoxes {
			return netip.Prefix{}, nil, refuse(http.StatusConflict, ReasonLimit, "%d session containers are running or starting, the most this stack runs at once (SF_MAX_BOXES=%d); stop one first", len(live), h.cfg.MaxBoxes)
		}
	}
	used, err := h.usedSubnets(ctx)
	if err != nil {
		return netip.Prefix{}, nil, err
	}
	for _, p := range h.pending {
		used = append(used, p)
	}
	for _, cand := range h.pool {
		free := !slices.ContainsFunc(used, cand.Overlaps)
		if !free {
			continue
		}
		h.pending[box] = cand
		return cand, func() {
			h.global.Lock()
			delete(h.pending, box)
			h.global.Unlock()
		}, nil
	}
	return netip.Prefix{}, nil, refuse(http.StatusServiceUnavailable, ReasonPool, "no /28 network in SF_BOX_SUBNETS (%s) is free for another session container", prefixes(h.cfg.Ranges))
}

func (h *Helper) waitHealthy(ctx context.Context, id string) (containerInspectJSON, error) {
	deadline := time.Now().Add(60 * time.Second)
	for {
		var ci containerInspectJSON
		if err := h.docker.get(ctx, "/containers/"+pathPart(id)+"/json", nil, &ci); err != nil {
			return ci, dockerFailed("inspect the box's container", err)
		}
		if ci.State.Running && ci.State.Health != nil && ci.State.Health.Status == healthHealthy {
			return ci, nil
		}
		if !ci.State.Running && ci.State.Status != "created" && ci.State.Status != "restarting" {
			return ci, refuse(http.StatusGatewayTimeout, ReasonUnhealthy, "the box's container stopped while it started (%s)", ci.State.Status)
		}
		if time.Now().After(deadline) {
			return ci, refuse(http.StatusGatewayTimeout, ReasonUnhealthy, "the box's agent-host didn't pass its health check within 60 seconds")
		}
		t := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ci, refuse(http.StatusGatewayTimeout, ReasonUnhealthy, "the box's start was cut short before its agent-host passed its health check")
		case <-t.C:
		}
	}
}
