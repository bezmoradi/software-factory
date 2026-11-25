package session

import (
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	CredentialToken    = "token"
	CredentialAWS      = "aws"
	CredentialSSH      = "ssh"
	CredentialFile     = "file"
	CredentialVars     = "vars"
	TagWrite           = "write"
	TagProduction      = "production"
	DeliveryHanded     = "handed"
	DeliverySigned     = "signing"
	DeliveryCopy       = "copy"
	MaxCredentials     = 32
	MaxCredentialBound = 16
	MaxCredentialVars  = 32
	MaxCredentialName  = 40
	MaxCredentialValue = 4096
	MaxCredentialKey   = 8 << 10
	MaxCredentialFile  = 16 << 10
	MaxCredentialPath  = 200
	MaxCredentialPass  = 512
	FileModePrivate    = "0600"
	FileModeShared     = "0644"
	SSHPassVariable    = "SSHPASS"
	maxHostKey         = 4096
)

var CredentialTypes = []GrantChoice{
	{Value: CredentialToken, Label: "Token"}, {Value: CredentialAWS, Label: "AWS keys"}, {Value: CredentialSSH, Label: "SSH"},
	{Value: CredentialFile, Label: "File"}, {Value: CredentialVars, Label: "Variables"},
}

var CredentialTags = []GrantChoice{{Value: TagWrite, Label: "write"}, {Value: TagProduction, Label: "production"}}

var CredentialModes = []GrantChoice{{Value: FileModePrivate, Label: "0600, only the agent reads it"}, {Value: FileModeShared, Label: "0644, anyone in the container reads it"}}

type CredentialVar struct {
	Name  string
	Value string
}

type CredentialSecret struct {
	Variable   string
	Value      string
	KeyID      string
	SecretKey  string
	Role       string
	Region     string
	User       string
	PrivateKey string
	Password   string
	HostKey    string
	Path       string
	Mode       string
	Contents   string
	Vars       []CredentialVar
}

type Credential struct {
	ID     string
	Name   string
	Type   string
	Bound  []string
	Tags   []string
	Saved  time.Time
	Secret CredentialSecret
}

type CredentialStore interface {
	Load() ([]Credential, error)
	Save([]Credential) error
}

var (
	credentialName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	variableName   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	awsKeyID       = regexp.MustCompile(`^(AKIA|ASIA)[A-Z0-9]{16}$`)
	awsSecret      = regexp.MustCompile(`^[A-Za-z0-9/+=]{40}$`)
	awsRole        = regexp.MustCompile(`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]{1,512}$`)
	awsRegion      = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)
	sshUser        = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
	sshHostKey     = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/]{16,}={0,3}$`)
	privateKeyPEM  = regexp.MustCompile(`^-----BEGIN ([A-Z0-9]+ )?PRIVATE KEY-----\n[A-Za-z0-9+/=\n:,\- ]+\n-----END ([A-Z0-9]+ )?PRIVATE KEY-----$`)
	homePath       = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+){0,15}$`)
)

var reservedVariables = []string{
	"PATH", "HOME", "SHELL", "USER", "LOGNAME", "PWD", "OLDPWD", "TERM", "LANG", "TMPDIR", "IFS", "ENV", "BASH_ENV", "NODE_OPTIONS",
	"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "ALL_PROXY", "LD_PRELOAD", "LD_LIBRARY_PATH", SSHPassVariable, "SSH_ASKPASS", "SSH_ASKPASS_REQUIRE",
}

var reservedVariablePrefixes = []string{"ANTHROPIC_", "CLAUDE_", "SF_", "LD_", "DYLD_", "BASH_FUNC_"}

var reservedHomePaths = []string{
	".claude", ".claude.json", ".config/software-factory", ".ssh", ".aws", ".gitconfig", ".config/git", ".profile", ".bashrc", ".bash_profile",
	".bash_login", ".bash_logout", ".zshenv", ".zprofile", ".zshrc", ".zlogin", ".config/fish",
}

type CredentialInfo struct {
	ID    string
	Name  string
	Type  string
	Label string
	Bound []string
	Tags  []string
	Saved time.Time
}

func credentialLabel(kind string) string {
	for _, c := range CredentialTypes {
		if c.Value == kind {
			return c.Label
		}
	}
	return kind
}

func (c Credential) Info() CredentialInfo {
	return CredentialInfo{ID: c.ID, Name: c.Name, Type: c.Type, Label: credentialLabel(c.Type), Bound: slices.Clone(c.Bound), Tags: slices.Clone(c.Tags), Saved: c.Saved}
}

func ModelCredentialText(s string) bool {
	return strings.Contains(s, "sk-ant-")
}

func (c Credential) secretTexts() []string {
	s := c.Secret
	out := []string{s.Value, s.KeyID, s.SecretKey, s.PrivateKey, s.Password, s.Contents}
	for _, v := range s.Vars {
		out = append(out, v.Value)
	}
	return out
}

func lineValue(v string, limit int) bool {
	return v != "" && len(v) <= limit && utf8.ValidString(v) && !hostReasonControl.MatchString(v)
}

func checkVariable(field, name string) error {
	if !variableName.MatchString(name) {
		return onField(field, invalid(field, "Needs capital letters, digits and _, like GH_TOKEN."))
	}
	if slices.Contains(reservedVariables, name) {
		return onField(field, invalid(field, withName("Needs another name; ", name, " is taken.")))
	}
	for _, p := range reservedVariablePrefixes {
		if strings.HasPrefix(name, p) {
			return onField(field, invalid(field, withName("Needs a name that doesn't start with ", p, ".")))
		}
	}
	return nil
}

func checkHomePath(p string) error {
	if !homePath.MatchString(p) || len(p) > MaxCredentialPath || slices.Contains(strings.Split(p, "/"), "..") || slices.Contains(strings.Split(p, "/"), ".") {
		return onField("path", invalid("path", "Needs a path under home like .kube/config, no .. part."))
	}
	for _, r := range reservedHomePaths {
		if p == r || strings.HasPrefix(p, r+"/") {
			return onField("path", invalid("path", withName("Needs another path; ~/", r, " holds settings.")))
		}
	}
	return nil
}

func normalCredential(c Credential) Credential {
	c.Name = strings.TrimSpace(c.Name)
	s := &c.Secret
	s.Variable = strings.TrimSpace(s.Variable)
	s.Value = strings.TrimSpace(s.Value)
	s.KeyID = strings.TrimSpace(s.KeyID)
	s.SecretKey = strings.TrimSpace(s.SecretKey)
	s.Role = strings.TrimSpace(s.Role)
	s.Region = strings.TrimSpace(s.Region)
	s.User = strings.TrimSpace(s.User)
	s.PrivateKey = strings.TrimSpace(strings.ReplaceAll(s.PrivateKey, "\r\n", "\n"))
	s.HostKey = strings.TrimSpace(s.HostKey)
	s.Path = strings.TrimPrefix(strings.TrimSpace(s.Path), "~/")
	s.Contents = strings.ReplaceAll(s.Contents, "\r\n", "\n")
	for i := range s.Vars {
		s.Vars[i].Name = strings.TrimSpace(s.Vars[i].Name)
		s.Vars[i].Value = strings.TrimSpace(s.Vars[i].Value)
	}
	return c
}

func checkSecret(c Credential) error {
	s := c.Secret
	switch c.Type {
	case CredentialToken:
		if err := checkVariable("variable", s.Variable); err != nil {
			return err
		}
		if !lineValue(s.Value, MaxCredentialValue) {
			return onField("value", invalid("value", "One line of 1 to 4,096 characters."))
		}
	case CredentialAWS:
		if !awsKeyID.MatchString(s.KeyID) {
			return onField("keyId", invalid("keyId", "Needs an access key id like AKIA followed by 16 more."))
		}
		if !awsSecret.MatchString(s.SecretKey) {
			return onField("secretKey", invalid("secretKey", "The 40-character secret access key."))
		}
		if s.Role != "" && !awsRole.MatchString(s.Role) {
			return onField("role", invalid("role", "Needs a role ARN like arn:aws:iam::123…:role/name."))
		}
		if !awsRegion.MatchString(s.Region) {
			return onField("region", invalid("region", "A region like eu-west-1."))
		}
	case CredentialSSH:
		if !sshUser.MatchString(s.User) {
			return onField("user", invalid("user", "Needs a user name like pi, at most 32 characters."))
		}
		switch {
		case (s.PrivateKey == "") == (s.Password == ""):
			return onField("privateKey", invalid("privateKey", "Give a private key or a password, not both."))
		case s.PrivateKey != "" && (len(s.PrivateKey) > MaxCredentialKey || !privateKeyPEM.MatchString(s.PrivateKey) || strings.Contains(s.PrivateKey, "ENCRYPTED")):
			return onField("privateKey", invalid("privateKey", "Needs an unencrypted private key, at most 8 KiB."))
		case s.Password != "" && !lineValue(s.Password, MaxCredentialPass):
			return onField("password", invalid("password", "One line of 1 to 512 characters."))
		}
		if s.HostKey != "" && (len(s.HostKey) > maxHostKey || !sshHostKey.MatchString(s.HostKey)) {
			return onField("hostKey", invalid("hostKey", "Needs one line like ssh-ed25519 AAAA…, or nothing."))
		}
	case CredentialFile:
		if err := checkHomePath(s.Path); err != nil {
			return err
		}
		if s.Mode != FileModePrivate && s.Mode != FileModeShared {
			return onField("mode", invalid("mode", "Choose 0600 or 0644."))
		}
		if s.Contents == "" || len(s.Contents) > MaxCredentialFile || !utf8.ValidString(s.Contents) || strings.ContainsRune(s.Contents, 0) {
			return onField("contents", invalid("contents", "Text of 1 byte to 16 KiB."))
		}
	case CredentialVars:
		if len(s.Vars) == 0 || len(s.Vars) > MaxCredentialVars {
			return onField("vars", invalid("vars", "Give 1 to 32 variables."))
		}
		seen := map[string]bool{}
		for _, v := range s.Vars {
			if err := checkVariable("vars", v.Name); err != nil {
				return err
			}
			if seen[v.Name] {
				return onField("vars", invalid("vars", withName("Needs each name once; ", v.Name, " is twice.")))
			}
			seen[v.Name] = true
			if !lineValue(v.Value, MaxCredentialValue) {
				return onField("vars", invalid("vars", withName("Needs one line of 1 to 4,096 characters for ", v.Name, ".")))
			}
		}
	default:
		return onField("type", invalid("type", "Choose Token, AWS keys, SSH, File or Variables."))
	}
	return nil
}

func keepTypeFields(c Credential) Credential {
	s, out := c.Secret, CredentialSecret{}
	switch c.Type {
	case CredentialToken:
		out.Variable, out.Value = s.Variable, s.Value
	case CredentialAWS:
		out.KeyID, out.SecretKey, out.Role, out.Region = s.KeyID, s.SecretKey, s.Role, s.Region
	case CredentialSSH:
		out.User, out.PrivateKey, out.Password, out.HostKey = s.User, s.PrivateKey, s.Password, s.HostKey
	case CredentialFile:
		out.Path, out.Mode, out.Contents = s.Path, s.Mode, s.Contents
	case CredentialVars:
		out.Vars = slices.Clone(s.Vars)
	}
	c.Secret = out
	return c
}

func (g *Grants) checkCredential(raw Credential) (Credential, error) {
	c := normalCredential(raw)
	if !credentialName.MatchString(c.Name) {
		return Credential{}, onField("name", invalid("name", "Needs small letters, digits, _ and -, like github-ci."))
	}
	if len(c.Bound) == 0 || len(c.Bound) > MaxCredentialBound {
		return Credential{}, onField("bound", invalid("bound", "Bind it to 1 to 16 hosts."))
	}
	bound := make([]string, 0, len(c.Bound))
	for _, raw := range c.Bound {
		name := HostName(raw)
		if h, p, err := net.SplitHostPort(name); err == nil {
			n, err := strconv.Atoi(p)
			if err != nil {
				n = -1
			}
			host, err := g.portRule(h, n)
			if err != nil {
				return Credential{}, onField("bound", err)
			}
			bound = append(bound, PortTarget(host, n))
			continue
		}
		if _, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
			return Credential{}, onField("bound", invalid("bound", withName("Needs a port with ", CleanLine(clip(raw, 60), 60), ", like :22.")))
		}
		if !ValidAllowEntry(name) {
			return Credential{}, onField("bound", invalid("bound", withName("Needs host names; ", CleanLine(clip(raw, 120), 120), " isn't one.")))
		}
		if ClaudeVendorHost(name) {
			return Credential{}, onField("bound", conflict("vendor", withName("Can't hold ", name, ", Claude Code's vendor.")))
		}
		if err := g.cfg.Allow.refuseOutside(name); err != nil {
			return Credential{}, onField("bound", err)
		}
		bound = append(bound, name)
	}
	c.Bound = sortedHosts(bound)
	tags := []string{}
	for _, t := range c.Tags {
		if !grantChoice(CredentialTags, t) {
			return Credential{}, onField("tags", invalid("tags", "Tags are write and production."))
		}
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	slices.Sort(tags)
	c.Tags = tags
	if err := checkSecret(c); err != nil {
		return Credential{}, err
	}
	c = keepTypeFields(c)
	if c.Delivery() == DeliverySigned {
		if _, err := g.cfg.SSH.PublicKey(c.Secret.PrivateKey); err != nil {
			return Credential{}, onField("privateKey", invalid("privateKey", "Needs an ed25519, ECDSA or RSA key ssh reads."))
		}
	}
	if slices.ContainsFunc(c.secretTexts(), ModelCredentialText) {
		return Credential{}, conflict("model credential", "That looks like an Anthropic API key or a Claude Code sign-in token. Software Factory never holds a model credential: sign Claude Code in on the page instead.")
	}
	return c, nil
}

func (c Credential) places() []string {
	s := c.Secret
	var out []string
	switch c.Type {
	case CredentialToken:
		out = append(out, "variable $"+s.Variable)
	case CredentialVars:
		for _, v := range s.Vars {
			out = append(out, "variable $"+v.Name)
		}
	case CredentialAWS:
		out = append(out, "AWS profile "+c.Name)
	case CredentialFile:
		out = append(out, "file ~/"+s.Path)
	case CredentialSSH:
		if s.Password != "" {
			out = append(out, "variable $"+SSHPassVariable)
		}
		for _, h := range c.Bound {
			out = append(out, "SSH host "+targetHost(h))
		}
	}
	return out
}

func (c Credential) Where() string {
	s := c.Secret
	switch c.Type {
	case CredentialToken:
		return "the variable $" + s.Variable + " in its shell commands"
	case CredentialVars:
		names := make([]string, 0, len(s.Vars))
		for _, v := range s.Vars {
			names = append(names, "$"+v.Name)
		}
		return "the variables " + strings.Join(names, ", ") + " in its shell commands"
	case CredentialAWS:
		return "short-lived copies of its keys, each for at most an hour, as the AWS profile " + c.Name + " in ~/.aws/credentials and ~/.aws/config (aws --profile " + c.Name + ")"
	case CredentialFile:
		return "the file ~/" + s.Path + " (mode " + s.Mode + ")"
	case CredentialSSH:
		if s.Password != "" {
			return "the variable $" + SSHPassVariable + ", which ssh reads for its hosts through SSH_ASKPASS, and a ~/.ssh/config entry for each of its hosts"
		}
		return "SSH logins as " + s.User + " that Software Factory signs through the agent at " + SSHAgentSocket + ", which ssh, scp and rsync use by themselves, and a ~/.ssh/config entry for each of its hosts; the key stays with Software Factory"
	}
	return ""
}

func (c Credential) Delivery() string {
	switch {
	case c.Type == CredentialSSH && c.Secret.PrivateKey != "":
		return DeliverySigned
	case c.Type == CredentialAWS:
		return DeliveryCopy
	}
	return DeliveryHanded
}

func (g *Grants) credentialByID(id string) (Credential, bool) {
	for _, c := range g.creds {
		if c.ID == id {
			return c, true
		}
	}
	return Credential{}, false
}

func (g *Grants) credentialByName(name string) (Credential, bool) {
	for _, c := range g.creds {
		if c.Name == name {
			return c, true
		}
	}
	return Credential{}, false
}

func ValidCredentialName(name string) bool {
	return credentialName.MatchString(name)
}

func ValidCredentialType(kind string) bool {
	return grantChoice(CredentialTypes, kind)
}

func ValidHomePath(p string) bool {
	return checkHomePath(p) == nil
}

func ValidSSHUser(user string) bool {
	return sshUser.MatchString(user)
}

func ValidCredentialVariable(name string) bool {
	return checkVariable("variable", name) == nil
}
