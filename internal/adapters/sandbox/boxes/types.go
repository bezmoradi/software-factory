package boxes

import "encoding/json"

type healthJSON struct {
	Test          []string `json:"Test"`
	Interval      int64    `json:"Interval"`
	Timeout       int64    `json:"Timeout"`
	StartPeriod   int64    `json:"StartPeriod"`
	StartInterval int64    `json:"StartInterval"`
	Retries       int      `json:"Retries"`
}

type restartJSON struct {
	Name              string `json:"Name"`
	MaximumRetryCount int    `json:"MaximumRetryCount"`
}

type logJSON struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config"`
}

type volumeOptionsJSON struct {
	NoCopy  bool   `json:"NoCopy,omitempty"`
	Subpath string `json:"Subpath,omitempty"`
}

type mountJSON struct {
	Type          string             `json:"Type"`
	Source        string             `json:"Source,omitempty"`
	Target        string             `json:"Target"`
	ReadOnly      bool               `json:"ReadOnly,omitempty"`
	VolumeOptions *volumeOptionsJSON `json:"VolumeOptions,omitempty"`
}

type hostCreateJSON struct {
	Init           bool              `json:"Init"`
	ReadonlyRootfs bool              `json:"ReadonlyRootfs"`
	CapDrop        []string          `json:"CapDrop"`
	Privileged     bool              `json:"Privileged"`
	SecurityOpt    []string          `json:"SecurityOpt"`
	OomScoreAdj    int               `json:"OomScoreAdj"`
	Memory         int64             `json:"Memory"`
	MemorySwap     int64             `json:"MemorySwap"`
	NanoCPUs       int64             `json:"NanoCpus"`
	PidsLimit      int64             `json:"PidsLimit"`
	RestartPolicy  restartJSON       `json:"RestartPolicy"`
	LogConfig      logJSON           `json:"LogConfig"`
	ExtraHosts     []string          `json:"ExtraHosts"`
	NetworkMode    string            `json:"NetworkMode"`
	Mounts         []mountJSON       `json:"Mounts"`
	Tmpfs          map[string]string `json:"Tmpfs"`
	Runtime        string            `json:"Runtime,omitempty"`
	CgroupnsMode   string            `json:"CgroupnsMode"`
	IpcMode        string            `json:"IpcMode"`
	ShmSize        int64             `json:"ShmSize"`
	GroupAdd       []string          `json:"GroupAdd,omitempty"`
}

type endpointCreateJSON struct {
	IPAMConfig ipamAddressJSON `json:"IPAMConfig"`
}

type ipamAddressJSON struct {
	IPv4Address string `json:"IPv4Address"`
}

type containerCreateJSON struct {
	Image            string            `json:"Image"`
	User             string            `json:"User"`
	Entrypoint       []string          `json:"Entrypoint,omitempty"`
	Cmd              []string          `json:"Cmd"`
	Env              []string          `json:"Env"`
	Labels           map[string]string `json:"Labels"`
	Healthcheck      healthJSON        `json:"Healthcheck"`
	HostConfig       hostCreateJSON    `json:"HostConfig"`
	NetworkingConfig struct {
		EndpointsConfig map[string]endpointCreateJSON `json:"EndpointsConfig"`
	} `json:"NetworkingConfig"`
}

type createdJSON struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

type hostInspectJSON struct {
	Init              *bool                      `json:"Init"`
	ReadonlyRootfs    bool                       `json:"ReadonlyRootfs"`
	CapAdd            []string                   `json:"CapAdd"`
	CapDrop           []string                   `json:"CapDrop"`
	Privileged        bool                       `json:"Privileged"`
	SecurityOpt       []string                   `json:"SecurityOpt"`
	OomScoreAdj       int                        `json:"OomScoreAdj"`
	Memory            int64                      `json:"Memory"`
	MemorySwap        int64                      `json:"MemorySwap"`
	NanoCPUs          int64                      `json:"NanoCpus"`
	PidsLimit         *int64                     `json:"PidsLimit"`
	RestartPolicy     restartJSON                `json:"RestartPolicy"`
	LogConfig         logJSON                    `json:"LogConfig"`
	ExtraHosts        []string                   `json:"ExtraHosts"`
	NetworkMode       string                     `json:"NetworkMode"`
	Mounts            []mountJSON                `json:"Mounts"`
	Tmpfs             map[string]string          `json:"Tmpfs"`
	Runtime           string                     `json:"Runtime"`
	CgroupnsMode      string                     `json:"CgroupnsMode"`
	Binds             []string                   `json:"Binds"`
	VolumesFrom       []string                   `json:"VolumesFrom"`
	Links             []string                   `json:"Links"`
	Devices           []json.RawMessage          `json:"Devices"`
	DeviceRequests    []json.RawMessage          `json:"DeviceRequests"`
	DeviceCgroupRules []string                   `json:"DeviceCgroupRules"`
	GroupAdd          []string                   `json:"GroupAdd"`
	Sysctls           map[string]string          `json:"Sysctls"`
	PortBindings      map[string]json.RawMessage `json:"PortBindings"`
	PublishAllPorts   bool                       `json:"PublishAllPorts"`
	MaskedPaths       []string                   `json:"MaskedPaths"`
	ReadonlyPaths     []string                   `json:"ReadonlyPaths"`
	IpcMode           string                     `json:"IpcMode"`
	ShmSize           int64                      `json:"ShmSize"`
	OomKillDisable    *bool                      `json:"OomKillDisable"`
	DNS               []string                   `json:"Dns"`
	DNSOptions        []string                   `json:"DnsOptions"`
	DNSSearch         []string                   `json:"DnsSearch"`
	PidMode           string                     `json:"PidMode"`
	UsernsMode        string                     `json:"UsernsMode"`
	UTSMode           string                     `json:"UTSMode"`
	CgroupParent      string                     `json:"CgroupParent"`
	Ulimits           []json.RawMessage          `json:"Ulimits"`
}

type mountPointJSON struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	Driver      string `json:"Driver"`
	Propagation string `json:"Propagation"`
	RW          bool   `json:"RW"`
}

type endpointJSON struct {
	IPAMConfig *ipamAddressJSON  `json:"IPAMConfig"`
	IPAddress  string            `json:"IPAddress"`
	NetworkID  string            `json:"NetworkID"`
	Aliases    []string          `json:"Aliases"`
	DriverOpts map[string]string `json:"DriverOpts"`
}

type containerInspectJSON struct {
	ID              string   `json:"Id"`
	Name            string   `json:"Name"`
	Image           string   `json:"Image"`
	AppArmorProfile string   `json:"AppArmorProfile"`
	ExecIDs         []string `json:"ExecIDs"`
	State           struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		ExitCode   int    `json:"ExitCode"`
		OOMKilled  bool   `json:"OOMKilled"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		User        string            `json:"User"`
		Cmd         []string          `json:"Cmd"`
		Entrypoint  []string          `json:"Entrypoint"`
		Env         []string          `json:"Env"`
		Labels      map[string]string `json:"Labels"`
		Healthcheck *healthJSON       `json:"Healthcheck"`
	} `json:"Config"`
	HostConfig      hostInspectJSON  `json:"HostConfig"`
	Mounts          []mountPointJSON `json:"Mounts"`
	NetworkSettings struct {
		Networks map[string]endpointJSON `json:"Networks"`
	} `json:"NetworkSettings"`
}

type containerListJSON struct {
	ID      string            `json:"Id"`
	ImageID string            `json:"ImageID"`
	Names   []string          `json:"Names"`
	Labels  map[string]string `json:"Labels"`
	State   string            `json:"State"`
}

type imageInspectJSON struct {
	ID          string   `json:"Id"`
	Os          string   `json:"Os"`
	RepoDigests []string `json:"RepoDigests"`
	Config      struct {
		Env        []string                   `json:"Env"`
		Entrypoint []string                   `json:"Entrypoint"`
		Labels     map[string]string          `json:"Labels"`
		Volumes    map[string]json.RawMessage `json:"Volumes"`
	} `json:"Config"`
}

type imageListJSON struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
}

type ipamConfigJSON struct {
	Subnet  string `json:"Subnet"`
	Gateway string `json:"Gateway,omitempty"`
}

type ipamJSON struct {
	Driver string           `json:"Driver"`
	Config []ipamConfigJSON `json:"Config"`
}

type networkCreateJSON struct {
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Internal   bool              `json:"Internal"`
	EnableIPv6 bool              `json:"EnableIPv6"`
	IPAM       ipamJSON          `json:"IPAM"`
	Options    map[string]string `json:"Options"`
	Labels     map[string]string `json:"Labels"`
}

type networkMemberJSON struct {
	Name        string `json:"Name"`
	IPv4Address string `json:"IPv4Address"`
}

type networkInspectJSON struct {
	ID         string                       `json:"Id"`
	Name       string                       `json:"Name"`
	Driver     string                       `json:"Driver"`
	Internal   bool                         `json:"Internal"`
	EnableIPv6 bool                         `json:"EnableIPv6"`
	IPAM       ipamJSON                     `json:"IPAM"`
	Options    map[string]string            `json:"Options"`
	Labels     map[string]string            `json:"Labels"`
	Containers map[string]networkMemberJSON `json:"Containers"`
}

type networkConnectJSON struct {
	Container      string             `json:"Container"`
	EndpointConfig endpointCreateJSON `json:"EndpointConfig"`
}

type networkDisconnectJSON struct {
	Container string `json:"Container"`
	Force     bool   `json:"Force"`
}

type volumeJSON struct {
	Name    string            `json:"Name"`
	Driver  string            `json:"Driver"`
	Labels  map[string]string `json:"Labels"`
	Options map[string]string `json:"Options"`
}

type volumeCreateJSON struct {
	Name   string            `json:"Name"`
	Driver string            `json:"Driver"`
	Labels map[string]string `json:"Labels"`
}

type volumeListJSON struct {
	Volumes []volumeJSON `json:"Volumes"`
}

type poolJSON struct {
	Base string `json:"Base"`
	Size int    `json:"Size"`
}

type infoJSON struct {
	Runtimes            map[string]json.RawMessage `json:"Runtimes"`
	DefaultRuntime      string                     `json:"DefaultRuntime"`
	DefaultAddressPools []poolJSON                 `json:"DefaultAddressPools"`
	OperatingSystem     string                     `json:"OperatingSystem"`
	OSType              string                     `json:"OSType"`
	Architecture        string                     `json:"Architecture"`
	KernelVersion       string                     `json:"KernelVersion"`
}

type eventJSON struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
}
