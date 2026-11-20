package boxes

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
)

const (
	DefaultMemory        = 2 << 30
	DefaultNanoCPUs      = 2_000_000_000
	DefaultSubnets       = "10.213.0.0/20"
	DefaultVerifyMemory  = 4 << 30
	DefaultVerifyDisk    = 2 << 30
	DefaultVerifyMinutes = 15
	maxVerifyMinutes     = 120
	RuntimeGVisor        = "runsc"
	boxBits              = 28
	maxPoolNetworks      = 4096
	maxBoxesLimit        = 256
)

type Config struct {
	Memory         int64
	NanoCPUs       int64
	ReviewerMemory int64
	ReviewerCPUs   int64
	VerifyMemory   int64
	VerifyCPUs     int64
	VerifyDisk     int64
	VerifyMinutes  int
	MaxBoxes       int
	Runtime        string
	Ranges         []netip.Prefix
	SweepDry       bool
	Live           bool
}

func ParseConfig(getenv func(string) string) (Config, error) {
	cfg := Config{}
	var err error
	if cfg.Memory, err = memoryOf(getenv, "SF_BOX_MEMORY"); err != nil {
		return Config{}, err
	}
	if cfg.NanoCPUs, err = cpusOf(getenv, "SF_BOX_CPUS"); err != nil {
		return Config{}, err
	}
	if cfg.ReviewerMemory, err = memoryOf(getenv, "SF_REVIEWER_MEMORY"); err != nil {
		return Config{}, err
	}
	if cfg.ReviewerCPUs, err = cpusOf(getenv, "SF_REVIEWER_CPUS"); err != nil {
		return Config{}, err
	}
	if err := verifyConfig(getenv, &cfg); err != nil {
		return Config{}, err
	}
	if v := strings.TrimSpace(getenv("SF_MAX_BOXES")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxBoxesLimit {
			return Config{}, fmt.Errorf("SF_MAX_BOXES must be a whole number from 1 to %d, or unset for no limit, got %q", maxBoxesLimit, v)
		}
		cfg.MaxBoxes = n
	}
	switch v := strings.TrimSpace(getenv("SF_BOX_RUNTIME")); v {
	case "":
	case RuntimeGVisor:
		cfg.Runtime = v
	default:
		return Config{}, fmt.Errorf("SF_BOX_RUNTIME must be unset or %s, got %q", RuntimeGVisor, v)
	}
	ranges, err := ParseSubnets(getenv)
	if err != nil {
		return Config{}, err
	}
	cfg.Ranges = ranges
	for _, sw := range []struct {
		key    string
		target *bool
	}{{"SF_BOX_SWEEP_DRY", &cfg.SweepDry}, {"SF_BOX_LIVE", &cfg.Live}} {
		switch v := strings.TrimSpace(getenv(sw.key)); v {
		case "", "0":
		case "1":
			*sw.target = true
		default:
			return Config{}, fmt.Errorf("%s must be 1, 0 or unset, got %q", sw.key, v)
		}
	}
	return cfg, nil
}

func verifyConfig(getenv func(string) string, cfg *Config) error {
	var err error
	if cfg.VerifyCPUs, err = cpusOf(getenv, "SF_VERIFY_CPUS"); err != nil {
		return err
	}
	cfg.VerifyMemory, cfg.VerifyDisk, cfg.VerifyMinutes = DefaultVerifyMemory, DefaultVerifyDisk, DefaultVerifyMinutes
	if v := strings.TrimSpace(getenv("SF_VERIFY_MEMORY")); v != "" {
		m, err := parseBytes(v)
		if err != nil || m < 256<<20 || m > 256<<30 {
			return fmt.Errorf("SF_VERIFY_MEMORY must be a size from 256m to 256g, such as 4g, got %q", v)
		}
		cfg.VerifyMemory = m
	}
	if v := strings.TrimSpace(getenv("SF_VERIFY_DISK")); v != "" {
		d, err := parseBytes(v)
		if err != nil || d < 64<<20 || d > 256<<30 {
			return fmt.Errorf("SF_VERIFY_DISK must be a size from 64m to 256g, such as 2g, got %q", v)
		}
		cfg.VerifyDisk = d
	}
	if cfg.VerifyDisk > cfg.VerifyMemory {
		cfg.VerifyDisk = cfg.VerifyMemory
	}
	if v := strings.TrimSpace(getenv("SF_VERIFY_MINUTES")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxVerifyMinutes {
			return fmt.Errorf("SF_VERIFY_MINUTES must be a whole number of minutes from 1 to %d, got %q", maxVerifyMinutes, v)
		}
		cfg.VerifyMinutes = n
	}
	return nil
}

func memoryOf(getenv func(string) string, key string) (int64, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return DefaultMemory, nil
	}
	m, err := parseBytes(v)
	if err != nil || m < 256<<20 || m > 256<<30 {
		return 0, fmt.Errorf("%s must be a size from 256m to 256g, such as 2g, got %q", key, v)
	}
	return m, nil
}

func cpusOf(getenv func(string) string, key string) (int64, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return DefaultNanoCPUs, nil
	}
	c, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsNaN(c) || c < 0.1 || c > 256 {
		return 0, fmt.Errorf("%s must be a number of CPUs from 0.1 to 256, such as 2, got %q", key, v)
	}
	return int64(math.Round(c * 1e9)), nil
}

func ParseSubnets(getenv func(string) string) ([]netip.Prefix, error) {
	subnets := strings.TrimSpace(getenv("SF_BOX_SUBNETS"))
	if subnets == "" {
		subnets = DefaultSubnets
	}
	return parseRanges(subnets)
}

func InPool(ranges []netip.Prefix, subnet netip.Prefix) bool {
	if !subnet.IsValid() || subnet.Bits() != boxBits {
		return false
	}
	for _, r := range ranges {
		if r.Contains(subnet.Addr()) {
			return true
		}
	}
	return false
}

func parseBytes(s string) (int64, error) {
	unit := int64(1)
	switch strings.ToLower(s[len(s)-1:]) {
	case "k":
		unit = 1 << 10
	case "m":
		unit = 1 << 20
	case "g":
		unit = 1 << 30
	}
	if unit != 1 {
		s = s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 || n > math.MaxInt64/unit {
		return 0, errors.New("not a size")
	}
	return n * unit, nil
}

func parseRanges(value string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	total := 0
	for item := range strings.SplitSeq(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		p, err := netip.ParsePrefix(item)
		if err != nil || !p.Addr().Is4() || p.Bits() < 8 || p.Bits() > boxBits || p.Masked() != p {
			return nil, fmt.Errorf("SF_BOX_SUBNETS must be IPv4 networks of /8 to /28 written at their first address, such as %s, got %q", DefaultSubnets, item)
		}
		for _, q := range out {
			if q.Overlaps(p) {
				return nil, fmt.Errorf("SF_BOX_SUBNETS lists %s and %s, which overlap", q, p)
			}
		}
		total += 1 << (boxBits - p.Bits())
		if total > maxPoolNetworks {
			return nil, fmt.Errorf("SF_BOX_SUBNETS holds more than %d networks of /28", maxPoolNetworks)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("SF_BOX_SUBNETS names no network")
	}
	return out, nil
}

func (c Config) pool() []netip.Prefix {
	var out []netip.Prefix
	for _, r := range c.Ranges {
		n := 1 << (boxBits - r.Bits())
		base := r.Addr().As4()
		start := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
		for i := range n {
			v := start + uint32(i)<<(32-boxBits)
			addr := netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
			out = append(out, netip.PrefixFrom(addr, boxBits))
		}
	}
	return out
}

func offset(p netip.Prefix, n int) netip.Addr {
	a := p.Addr()
	for range n {
		a = a.Next()
	}
	return a
}

func gatewayOf(p netip.Prefix) netip.Addr { return offset(p, 1) }
func engineOf(p netip.Prefix) netip.Addr  { return offset(p, 2) }
func egressOf(p netip.Prefix) netip.Addr  { return offset(p, 3) }
func agentOf(p netip.Prefix) netip.Addr   { return offset(p, 4) }
