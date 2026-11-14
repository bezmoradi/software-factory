package boxes

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/bezmoradi/software-factory/internal/core/shared"
)

const (
	SocketPath     = "/var/run/docker.sock"
	DevMarker      = "/usr/share/software-factory/dev-image"
	helperService  = "helper"
	engineService  = "engine"
	egressService  = "egress"
	volumeWork     = "workspace"
	volumeAgents   = "agents"
	volumeUploads  = "uploads"
	volumeDevBin   = "dev-bin"
	boxPrefix      = "-box-"
	homePrefix     = "-home-"
	oneOffTrue     = "True"
	stateRunning   = "running"
	healthHealthy  = "healthy"
	runtimeDefault = "runc"
)

var hostnamePattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

type HelperConfig struct {
	Docker    *Docker
	Config    Config
	Hostname  string
	DevMarker string
	Logger    shared.Logger
}

type Helper struct {
	docker  *Docker
	cfg     Config
	rec     *recipe
	self    string
	pool    []netip.Prefix
	logger  shared.Logger
	locks   *keyedLocks
	images  sync.Mutex
	labels  map[string]map[string]string
	global  sync.Mutex
	pending map[string]netip.Prefix
	kick    chan struct{}
}

var _ session.Boxes = (*Helper)(nil)

func NewHelper(ctx context.Context, hc HelperConfig) (*Helper, error) {
	if hc.Docker == nil || hc.Logger == nil {
		return nil, errors.New("box helper: needs Docker and a logger")
	}
	version, err := hc.Docker.CheckVersion(ctx)
	if err != nil {
		return nil, err
	}
	if !hostnamePattern.MatchString(hc.Hostname) {
		return nil, fmt.Errorf("the box helper finds its own container by HOSTNAME, which must be the container ID Docker sets, got %q", hc.Hostname)
	}
	var self containerInspectJSON
	if err := hc.Docker.get(ctx, "/containers/"+pathPart(hc.Hostname)+"/json", nil, &self); err != nil {
		return nil, fmt.Errorf("the box helper can't inspect its own container %s: %w", hc.Hostname, err)
	}
	labels := self.Config.Labels
	if labels[labelService] != helperService {
		return nil, fmt.Errorf("the box helper runs only as the Compose service %q, and its container's service label is %q", helperService, labels[labelService])
	}
	project := labels[labelProject]
	if !session.ValidComposeProject(project) {
		return nil, fmt.Errorf("the box helper's container has no Compose project label it can use, got %q", project)
	}
	if labels[labelOneOff] == oneOffTrue {
		return nil, errors.New("the box helper doesn't run as a one-off container (docker compose run)")
	}
	if !hasSocketMount(self.Mounts) {
		return nil, fmt.Errorf("the box helper's container has no bind mount of Docker's socket at %s", SocketPath)
	}
	if !strings.HasPrefix(self.Image, "sha256:") {
		return nil, fmt.Errorf("the box helper's container shows no image ID, got %q", self.Image)
	}
	if hc.Config.Live {
		marker := hc.DevMarker
		if marker == "" {
			marker = DevMarker
		}
		if st, err := os.Stat(marker); err != nil || !st.Mode().IsRegular() {
			hc.Config.Live = false
			hc.Logger.Warn("box helper", "event", "live_ignored", "why", "SF_BOX_LIVE=1 runs a binary from the dev-bin volume in every box, so the box helper honours it only in a development image, and this image has no "+marker)
		}
	}
	var img imageInspectJSON
	if err := hc.Docker.get(ctx, "/images/"+pathPart(self.Image)+"/json", nil, &img); err != nil {
		return nil, fmt.Errorf("the box helper can't inspect its own image %s: %w", self.Image, err)
	}
	var info infoJSON
	if err := hc.Docker.get(ctx, "/info", nil, &info); err != nil {
		return nil, fmt.Errorf("the box helper can't read Docker's system information: %w", err)
	}
	runtime := info.DefaultRuntime
	if runtime == "" {
		runtime = runtimeDefault
	}
	if hc.Config.Runtime != "" {
		runtime = hc.Config.Runtime
	}
	h := &Helper{
		docker: hc.Docker, cfg: hc.Config, self: self.ID, pool: hc.Config.pool(), logger: hc.Logger,
		locks: newKeyedLocks(), labels: map[string]map[string]string{self.Image: img.Config.Labels}, pending: map[string]netip.Prefix{}, kick: make(chan struct{}, 1),
		rec: &recipe{project: project, image: self.Image, imageEnv: img.Config.Env, imageLabel: img.Config.Labels, entrypoint: img.Config.Entrypoint, runtime: runtime, cfg: hc.Config},
	}
	if err := h.checkPool(ctx, info); err != nil {
		return nil, err
	}
	if hc.Config.Runtime != "" {
		if _, ok := info.Runtimes[hc.Config.Runtime]; !ok {
			h.logger.Warn("box helper", "event", "runtime_missing", "runtime", hc.Config.Runtime, "fix", "Docker lists no "+hc.Config.Runtime+" runtime, so every box start is refused until it does")
		}
	}
	h.logger.Info("box helper ready", "project", project, "image", self.Image, "docker", version, "api", APIVersion,
		"memory", hc.Config.Memory, "nano_cpus", hc.Config.NanoCPUs, "max_boxes", hc.Config.MaxBoxes, "runtime", runtime,
		"subnets", prefixes(hc.Config.Ranges), "sweep_dry", hc.Config.SweepDry, "live", hc.Config.Live)
	return h, nil
}

func hasSocketMount(mounts []mountPointJSON) bool {
	for _, m := range mounts {
		if m.Type == "bind" && m.Destination == SocketPath {
			return true
		}
	}
	return false
}

func prefixes(list []netip.Prefix) string {
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.String()
	}
	return strings.Join(out, ",")
}

func defaultPools(info infoJSON) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range info.DefaultAddressPools {
		if q, err := netip.ParsePrefix(p.Base); err == nil && q.Addr().Is4() {
			out = append(out, q.Masked())
		}
	}
	if len(out) == 0 {
		out = []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16")}
	}
	return out
}

func (h *Helper) checkPool(ctx context.Context, info infoJSON) error {
	for _, r := range h.cfg.Ranges {
		for _, p := range defaultPools(info) {
			if r.Overlaps(p) {
				return fmt.Errorf("SF_BOX_SUBNETS %s overlaps Docker's default address pool %s, from which Docker gives other networks their addresses; set SF_BOX_SUBNETS to a range outside it", r, p)
			}
		}
	}
	var nets []networkInspectJSON
	if err := h.docker.get(ctx, "/networks", nil, &nets); err != nil {
		return fmt.Errorf("the box helper can't list Docker's networks: %w", err)
	}
	for _, n := range nets {
		_, ours := ownedName(h.rec.project, boxPrefix, n.Name, n.Labels)
		for _, s := range subnetsOf(n) {
			for _, r := range h.cfg.Ranges {
				if !r.Overlaps(s) {
					continue
				}
				if ours && s.Bits() == boxBits && r.Contains(s.Addr()) {
					continue
				}
				return fmt.Errorf("SF_BOX_SUBNETS %s overlaps the Docker network %s (%s); set SF_BOX_SUBNETS to a range no other network uses", r, n.Name, s)
			}
		}
	}
	return nil
}

func subnetsOf(n networkInspectJSON) []netip.Prefix {
	var out []netip.Prefix
	for _, c := range n.IPAM.Config {
		if p, err := netip.ParsePrefix(c.Subnet); err == nil && p.Addr().Is4() {
			out = append(out, p.Masked())
		}
	}
	return out
}

func boxSubnet(n *networkInspectJSON) (netip.Prefix, bool) {
	if n == nil {
		return netip.Prefix{}, false
	}
	s := subnetsOf(*n)
	if len(s) != 1 || s[0].Bits() != boxBits {
		return netip.Prefix{}, false
	}
	return s[0], true
}
