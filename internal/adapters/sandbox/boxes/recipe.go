package boxes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	LabelStack     = "dev.software-factory.stack"
	LabelBox       = "dev.software-factory.box"
	LabelRecipe    = "dev.software-factory.recipe"
	composeLabels  = "com.docker.compose."
	labelProject   = "com.docker.compose.project"
	labelService   = "com.docker.compose.service"
	labelVolume    = "com.docker.compose.volume"
	labelOneOff    = "com.docker.compose.oneoff"
	gatewayModeKey = "com.docker.network.bridge.gateway_mode_ipv4"
	gatewayIsolate = "isolated"
	boxUser        = "10002:10000"
	agentHostPort  = "8090"
	boxWorkspace   = "/workspace/s/"
	boxUploads     = "/uploads/"
	agentsTarget   = "/opt/software-factory/agents"
	homeTarget     = "/home/agent"
	liveTarget     = "/opt/sf-dev/bin"
	liveEntrypoint = liveTarget + "/software-factory"
	proxyURL       = "http://" + session.PortProxy
	noProxy        = "localhost,127.0.0.1,::1"
	boxOOMScore    = 500
	boxPids        = 2048
	boxShm         = 64 << 20
	ipcPrivate     = "private"
	apparmorOff    = "unconfined"
	volumeDriver   = "local"
)

var (
	dockerMasked   = []string{"/proc/acpi", "/proc/asound", "/proc/kcore", "/proc/keys", "/proc/latency_stats", "/proc/sched_debug", "/proc/scsi", "/proc/timer_list", "/proc/timer_stats", "/sys/firmware"}
	dockerReadonly = []string{"/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger"}
)

type stackVolumes struct {
	Workspace, Agents, Uploads, DevBin string
}

type recipe struct {
	project    string
	image      string
	imageEnv   []string
	imageLabel map[string]string
	entrypoint []string
	runtime    string
	cfg        Config
}

func containerName(project, box string) string { return project + "-box-" + box }
func networkName(project, box string) string   { return project + "-box-" + box }
func homeName(project, box string) string      { return project + "-home-" + box }

func boxEnv(box string) []string {
	return []string{
		"SF_WORKSPACE=" + boxWorkspace + box,
		"HOME=" + homeTarget,
		"SF_AGENT_HOST_LISTEN=:" + agentHostPort,
		"SF_ENGINE_AGENT_ADDRESS=engine-agents:8081",
		"SF_PLUGIN_SOCKET=/run/sf/plugin.sock",
		"SF_UMASK=0002",
		"HTTPS_PROXY=" + proxyURL,
		"HTTP_PROXY=" + proxyURL,
		"https_proxy=" + proxyURL,
		"http_proxy=" + proxyURL,
		"NO_PROXY=" + noProxy,
		"no_proxy=" + noProxy,
		"DISABLE_AUTOUPDATER=1",
	}
}

func boxHealth() healthJSON {
	return healthJSON{
		Test:          []string{"CMD", "curl", "-fsS", "--noproxy", "*", "-o", "/dev/null", "http://127.0.0.1:" + agentHostPort + "/healthz"},
		Interval:      int64(30 * time.Second),
		Timeout:       int64(5 * time.Second),
		StartPeriod:   int64(60 * time.Second),
		StartInterval: int64(time.Second),
		Retries:       3,
	}
}

func (r *recipe) mounts(box string, vols stackVolumes) []mountJSON {
	out := []mountJSON{
		{Type: "volume", Source: vols.Workspace, Target: boxWorkspace + box, VolumeOptions: &volumeOptionsJSON{NoCopy: true, Subpath: "s/" + box}},
		{Type: "volume", Source: vols.Agents, Target: agentsTarget, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true}},
		{Type: "volume", Source: homeName(r.project, box), Target: homeTarget},
		{Type: "volume", Source: vols.Uploads, Target: boxUploads + box, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true, Subpath: box}},
		{Type: "volume", Source: vols.Workspace, Target: session.CopiesAt, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true, Subpath: session.CopiesFolder + "/" + box}},
		{Type: "volume", Source: vols.Workspace, Target: session.ScratchAt, VolumeOptions: &volumeOptionsJSON{NoCopy: true, Subpath: "s/" + box + "/" + session.ScratchFolderName}},
	}
	if r.cfg.Live {
		out = append(out, mountJSON{Type: "volume", Source: vols.DevBin, Target: liveTarget, ReadOnly: true, VolumeOptions: &volumeOptionsJSON{NoCopy: true}})
	}
	return out
}

func (r *recipe) labels(box string) map[string]string {
	return map[string]string{LabelStack: r.project, LabelBox: box}
}

func (r *recipe) containerLabels(box string) map[string]string {
	return r.blanked(r.labels(box))
}

func (r *recipe) blanked(out map[string]string) map[string]string {
	for k := range r.imageLabel {
		if strings.HasPrefix(k, composeLabels) {
			out[k] = ""
		}
	}
	return out
}

func (r *recipe) create(box string, subnet netip.Prefix, vols stackVolumes) containerCreateJSON {
	net := networkName(r.project, box)
	c := containerCreateJSON{
		Image:       r.image,
		User:        boxUser,
		Cmd:         []string{"agent-host"},
		Env:         boxEnv(box),
		Labels:      r.containerLabels(box),
		Healthcheck: boxHealth(),
		HostConfig: hostCreateJSON{
			Init:           true,
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			OomScoreAdj:    boxOOMScore,
			Memory:         r.cfg.Memory,
			MemorySwap:     r.cfg.Memory,
			NanoCPUs:       r.cfg.NanoCPUs,
			PidsLimit:      boxPids,
			RestartPolicy:  restartJSON{Name: "on-failure", MaximumRetryCount: 3},
			LogConfig:      logJSON{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
			ExtraHosts:     []string{"engine-agents:" + engineOf(subnet).String(), "egress:" + egressOf(subnet).String(), "egress-agents:" + egressOf(subnet).String()},
			NetworkMode:    net,
			Mounts:         r.mounts(box, vols),
			Tmpfs:          map[string]string{"/tmp": "size=1g,mode=1777", "/run/sf": "size=1m,mode=0700,uid=10002,gid=10000"},
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

type mountView struct {
	Type, Source, Target, Subpath string
	ReadOnly, NoCopy              bool
}

type pointView struct {
	Type, Name, Destination, Driver, Propagation string
	RW                                           bool
}

type recipeView struct {
	Image           string
	User            string
	Entrypoint      []string
	Cmd             []string
	Env             []string
	Labels          map[string]string
	Health          healthJSON
	Init            bool
	ReadonlyRootfs  bool
	CapAdd          []string
	CapDrop         []string
	Privileged      bool
	SecurityOpt     []string
	AppArmorOff     bool
	MaskedMissing   []string
	ReadonlyMissing []string
	OomScoreAdj     int
	OomKillOff      bool
	Memory          int64
	MemorySwap      int64
	NanoCPUs        int64
	PidsLimit       int64
	Restart         restartJSON
	LogType         string
	LogConfig       map[string]string
	ExtraHosts      []string
	NetworkMode     string
	Networks        []string
	Address         string
	OtherAliases    []string
	EndpointOpts    map[string]string
	Mounts          []mountView
	Points          []pointView
	Tmpfs           map[string]string
	Runtime         string
	CgroupnsMode    string
	IpcMode         string
	ShmSize         int64
	DNS             []string
	DNSOptions      []string
	DNSSearch       []string
	PidMode         string
	UsernsMode      string
	UTSMode         string
	CgroupParent    string
	Binds           []string
	VolumesFrom     []string
	Links           []string
	Devices         int
	DeviceRequests  int
	DeviceRules     []string
	GroupAdd        []string
	Sysctls         map[string]string
	PortBindings    int
	PublishAll      bool
}

type generic struct {
	names                 *strings.Replacer
	engine, egress, agent string
	logKeys               []string
}

func (r *recipe) genericFor(box string, subnet netip.Prefix) generic {
	return genericOf(subnet, networkName(r.project, box), "{network}", homeName(r.project, box), "{home}", box, "{box}")
}

func genericOf(subnet netip.Prefix, pairs ...string) generic {
	return generic{
		names:   strings.NewReplacer(pairs...),
		engine:  engineOf(subnet).String(),
		egress:  egressOf(subnet).String(),
		agent:   agentOf(subnet).String(),
		logKeys: []string{"max-file", "max-size"},
	}
}

func (g generic) name(s string) string { return g.names.Replace(s) }

func (g generic) address(s string) string {
	switch s {
	case g.engine:
		return "{engine}"
	case g.egress:
		return "{egress}"
	case g.agent:
		return "{agent}"
	}
	return s
}

func (g generic) host(entry string) string {
	i := strings.LastIndexByte(entry, ':')
	if i < 0 {
		return entry
	}
	return entry[:i+1] + g.address(entry[i+1:])
}

func (g generic) all(list []string, f func(string) string) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = f(s)
	}
	slices.Sort(out)
	return out
}

func (g generic) mountsOf(list []mountJSON) []mountView {
	var out []mountView
	for _, m := range list {
		v := mountView{Type: m.Type, Source: g.name(m.Source), Target: g.name(m.Target), ReadOnly: m.ReadOnly}
		if m.VolumeOptions != nil {
			v.NoCopy, v.Subpath = m.VolumeOptions.NoCopy, g.name(m.VolumeOptions.Subpath)
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b mountView) int { return strings.Compare(a.Target, b.Target) })
	return out
}

func (g generic) logOf(cfg map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range g.logKeys {
		out[k] = cfg[k]
	}
	return out
}

func (r *recipe) labelsWanted(box string) map[string]string {
	return r.wanted(r.containerLabels(box))
}

func (r *recipe) wanted(labels map[string]string) map[string]string {
	out := maps.Clone(r.imageLabel)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, labels)
	return out
}

func withoutRecipe(labels map[string]string) map[string]string {
	out := maps.Clone(labels)
	delete(out, LabelRecipe)
	return out
}

func composeBlank(labels map[string]string) bool {
	for k, v := range labels {
		if strings.HasPrefix(k, composeLabels) && v != "" {
			return false
		}
	}
	return true
}

func composeMatches(labels, image map[string]string) bool {
	for k, v := range labels {
		if strings.HasPrefix(k, composeLabels) {
			if iv, ok := image[k]; !ok || iv != v {
				return false
			}
		}
	}
	return true
}

func otherAliases(aliases []string, id, name string) []string {
	short := id
	if len(short) > 12 {
		short = short[:12]
	}
	var out []string
	for _, a := range aliases {
		if a != short && a != id && a != name {
			out = append(out, a)
		}
	}
	slices.Sort(out)
	return out
}

func missingFrom(got, want []string) []string {
	var out []string
	for _, p := range want {
		if !slices.Contains(got, p) {
			out = append(out, p)
		}
	}
	return out
}

func mergeEnv(image, ours []string) []string {
	out := slices.Clone(image)
	for _, e := range ours {
		key, _, _ := strings.Cut(e, "=")
		replaced := false
		for i, x := range out {
			if k, _, _ := strings.Cut(x, "="); k == key {
				out[i], replaced = e, true
			}
		}
		if !replaced {
			out = append(out, e)
		}
	}
	return out
}

func (r *recipe) expectedView(box string, subnet netip.Prefix, vols stackVolumes) recipeView {
	return r.viewOf(r.create(box, subnet, vols), r.labelsWanted(box), r.genericFor(box, subnet), subnet)
}

func (r *recipe) viewOf(c containerCreateJSON, labels map[string]string, g generic, subnet netip.Prefix) recipeView {
	entry := r.entrypoint
	if c.Entrypoint != nil {
		entry = c.Entrypoint
	}
	var points []pointView
	for _, m := range c.HostConfig.Mounts {
		points = append(points, pointView{Type: m.Type, Name: g.name(m.Source), Destination: g.name(m.Target), Driver: volumeDriver, RW: !m.ReadOnly})
	}
	slices.SortFunc(points, func(a, b pointView) int { return strings.Compare(a.Destination, b.Destination) })
	h := c.HostConfig
	return recipeView{
		Image: c.Image, User: c.User, Entrypoint: slices.Clone(entry), Cmd: c.Cmd,
		Env:    g.all(mergeEnv(r.imageEnv, c.Env), g.name),
		Labels: labelsView(labels, g), Health: c.Healthcheck,
		Init: h.Init, ReadonlyRootfs: h.ReadonlyRootfs, CapDrop: h.CapDrop, Privileged: h.Privileged, SecurityOpt: h.SecurityOpt,
		OomScoreAdj: h.OomScoreAdj, Memory: h.Memory, MemorySwap: h.MemorySwap, NanoCPUs: h.NanoCPUs, PidsLimit: h.PidsLimit,
		Restart: h.RestartPolicy, LogType: h.LogConfig.Type, LogConfig: g.logOf(h.LogConfig.Config),
		ExtraHosts: g.all(h.ExtraHosts, g.host), NetworkMode: g.name(h.NetworkMode),
		Networks: []string{g.name(h.NetworkMode)}, Address: g.address(agentOf(subnet).String()),
		Mounts: g.mountsOf(h.Mounts), Points: points, Tmpfs: h.Tmpfs, Runtime: r.runtime, CgroupnsMode: h.CgroupnsMode,
		IpcMode: h.IpcMode, ShmSize: h.ShmSize, GroupAdd: nonEmpty(h.GroupAdd),
	}
}

func labelsView(labels map[string]string, g generic) map[string]string {
	out := map[string]string{}
	for k, v := range labels {
		out[k] = g.name(v)
	}
	return out
}

func (r *recipe) inspectedView(ci containerInspectJSON, box string, subnet netip.Prefix) recipeView {
	return inspectedIn(ci, r.genericFor(box, subnet), networkName(r.project, box))
}

func inspectedIn(ci containerInspectJSON, g generic, own string) recipeView {
	h := ci.HostConfig
	var points []pointView
	for _, p := range ci.Mounts {
		points = append(points, pointView{Type: p.Type, Name: g.name(p.Name), Destination: g.name(p.Destination), Driver: p.Driver, Propagation: p.Propagation, RW: p.RW})
	}
	slices.SortFunc(points, func(a, b pointView) int { return strings.Compare(a.Destination, b.Destination) })
	var networks, aliases []string
	var opts map[string]string
	address := ""
	for name, ep := range ci.NetworkSettings.Networks {
		networks = append(networks, g.name(name))
		if name != own {
			continue
		}
		if ep.IPAMConfig != nil {
			address = g.address(ep.IPAMConfig.IPv4Address)
		}
		aliases = otherAliases(ep.Aliases, ci.ID, strings.TrimPrefix(ci.Name, "/"))
		if len(ep.DriverOpts) > 0 {
			opts = ep.DriverOpts
		}
	}
	slices.Sort(networks)
	v := recipeView{
		Image: ci.Image, User: ci.Config.User, Entrypoint: ci.Config.Entrypoint, Cmd: ci.Config.Cmd,
		Env:            g.all(ci.Config.Env, g.name),
		Labels:         labelsView(withoutRecipe(ci.Config.Labels), g),
		ReadonlyRootfs: h.ReadonlyRootfs, CapAdd: nonEmpty(h.CapAdd), CapDrop: h.CapDrop, Privileged: h.Privileged, SecurityOpt: h.SecurityOpt,
		AppArmorOff:   ci.AppArmorProfile == apparmorOff,
		MaskedMissing: missingFrom(h.MaskedPaths, dockerMasked), ReadonlyMissing: missingFrom(h.ReadonlyPaths, dockerReadonly),
		OomScoreAdj: h.OomScoreAdj, OomKillOff: h.OomKillDisable != nil && *h.OomKillDisable, Memory: h.Memory, MemorySwap: h.MemorySwap, NanoCPUs: h.NanoCPUs,
		Restart: h.RestartPolicy, LogType: h.LogConfig.Type, LogConfig: g.logOf(h.LogConfig.Config),
		ExtraHosts: g.all(h.ExtraHosts, g.host), NetworkMode: g.name(h.NetworkMode), Networks: networks, Address: address,
		OtherAliases: aliases, EndpointOpts: opts,
		Mounts: g.mountsOf(h.Mounts), Points: points, Tmpfs: h.Tmpfs, Runtime: h.Runtime, CgroupnsMode: h.CgroupnsMode,
		IpcMode: h.IpcMode, ShmSize: h.ShmSize,
		DNS: nonEmpty(h.DNS), DNSOptions: nonEmpty(h.DNSOptions), DNSSearch: nonEmpty(h.DNSSearch),
		PidMode: h.PidMode, UsernsMode: h.UsernsMode, UTSMode: h.UTSMode, CgroupParent: h.CgroupParent,
		Binds: nonEmpty(h.Binds), VolumesFrom: nonEmpty(h.VolumesFrom), Links: nonEmpty(h.Links),
		Devices: len(h.Devices), DeviceRequests: len(h.DeviceRequests), DeviceRules: nonEmpty(h.DeviceCgroupRules), GroupAdd: nonEmpty(h.GroupAdd),
		PortBindings: len(h.PortBindings), PublishAll: h.PublishAllPorts,
	}
	if len(h.Sysctls) > 0 {
		v.Sysctls = h.Sysctls
	}
	if ci.Config.Healthcheck != nil {
		v.Health = *ci.Config.Healthcheck
	}
	if h.Init != nil {
		v.Init = *h.Init
	}
	if h.PidsLimit != nil {
		v.PidsLimit = *h.PidsLimit
	}
	return v
}

func nonEmpty(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	return list
}

func hashOf(v recipeView) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func differences(want, got recipeView) []string {
	var out []string
	wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
	t := wv.Type()
	for i := range t.NumField() {
		a, _ := json.Marshal(wv.Field(i).Interface())
		b, _ := json.Marshal(gv.Field(i).Interface())
		if string(a) != string(b) {
			out = append(out, t.Field(i).Name)
		}
	}
	return out
}

func validRecipeLabel(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

func ownedName(project, prefix, name string, labels map[string]string) (string, bool) {
	box := labels[LabelBox]
	if labels[LabelStack] != project || !session.ValidBox(box) || name != project+prefix+box {
		return "", false
	}
	return box, true
}
