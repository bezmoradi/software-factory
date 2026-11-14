package npm

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/bits"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	SLSAProvenance  = "https://slsa.dev/provenance/v1"
	inTotoStatement = "https://in-toto.io/Statement/v1"
	inTotoPayload   = "application/vnd.in-toto+json"
	githubIssuer    = "https://token.actions.githubusercontent.com"
	rekorOrigin     = "rekor.sigstore.dev - "
)

const rekorKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE2G2Y+2tabdTV5BcGiBIx0a9fAFwr
kBbmLSGtks4L3qX6yYY0zufBnhC8Ur/iy55GhWP/9A/bY2LhC30M9+RYtw==
-----END PUBLIC KEY-----`

const fulcioIntermediatePEM = `-----BEGIN CERTIFICATE-----
MIICGjCCAaGgAwIBAgIUALnViVfnU0brJasmRkHrn/UnfaQwCgYIKoZIzj0EAwMw
KjEVMBMGA1UEChMMc2lnc3RvcmUuZGV2MREwDwYDVQQDEwhzaWdzdG9yZTAeFw0y
MjA0MTMyMDA2MTVaFw0zMTEwMDUxMzU2NThaMDcxFTATBgNVBAoTDHNpZ3N0b3Jl
LmRldjEeMBwGA1UEAxMVc2lnc3RvcmUtaW50ZXJtZWRpYXRlMHYwEAYHKoZIzj0C
AQYFK4EEACIDYgAE8RVS/ysH+NOvuDZyPIZtilgUF9NlarYpAd9HP1vBBH1U5CV7
7LSS7s0ZiH4nE7Hv7ptS6LvvR/STk798LVgMzLlJ4HeIfF3tHSaexLcYpSASr1kS
0N/RgBJz/9jWCiXno3sweTAOBgNVHQ8BAf8EBAMCAQYwEwYDVR0lBAwwCgYIKwYB
BQUHAwMwEgYDVR0TAQH/BAgwBgEB/wIBADAdBgNVHQ4EFgQU39Ppz1YkEZb5qNjp
KFWixi4YZD8wHwYDVR0jBBgwFoAUWMAeX5FFpWapesyQoZMi0CrFxfowCgYIKoZI
zj0EAwMDZwAwZAIwPCsQK4DYiZYDPIaDi5HFKnfxXx6ASSVmERfsynYBiX2X6SJR
nZU84/9DZdnFvvxmAjBOt6QpBlc4J/0DxvkTCqpclvziL6BCCPnjdlIB3Pu3BxsP
mygUY7Ii2zbdCdliiow=
-----END CERTIFICATE-----`

const fulcioRootPEM = `-----BEGIN CERTIFICATE-----
MIIB9zCCAXygAwIBAgIUALZNAPFdxHPwjeDloDwyYChAO/4wCgYIKoZIzj0EAwMw
KjEVMBMGA1UEChMMc2lnc3RvcmUuZGV2MREwDwYDVQQDEwhzaWdzdG9yZTAeFw0y
MTEwMDcxMzU2NTlaFw0zMTEwMDUxMzU2NThaMCoxFTATBgNVBAoTDHNpZ3N0b3Jl
LmRldjERMA8GA1UEAxMIc2lnc3RvcmUwdjAQBgcqhkjOPQIBBgUrgQQAIgNiAAT7
XeFT4rb3PQGwS4IajtLk3/OlnpgangaBclYpsYBr5i+4ynB07ceb3LP0OIOZdxex
X69c5iVuyJRQ+Hz05yi+UF3uBWAlHpiS5sh0+H2GHE7SXrk1EC5m1Tr19L9gg92j
YzBhMA4GA1UdDwEB/wQEAwIBBjAPBgNVHRMBAf8EBTADAQH/MB0GA1UdDgQWBBRY
wB5fkUWlZql6zJChkyLQKsXF+jAfBgNVHSMEGDAWgBRYwB5fkUWlZql6zJChkyLQ
KsXF+jAKBggqhkjOPQQDAwNpADBmAjEAj1nHeXZp+13NWBNa+EDsDP8G1WWg1tCM
WP/WHPqpaVo0jhsweNFZgSs0eE7wYI4qAjEA2WB9ot98sIkoF3vZYdd3/VtWB5b9
TNMea7Ix/stJ5TfcLLeABLE4BNJOsQ4vnBHJ
-----END CERTIFICATE-----`

var (
	oidIssuer     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
	oidSigner     = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 9}
	oidSourceRepo = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 12}
	oidSourceSHA  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 13}
	oidSourceRef  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 14}
)

type Source struct {
	Repository string
	Workflow   string
	Ref        string
}

func (s Source) Set() bool { return s.Repository != "" }

type Verified struct {
	Repository string
	Workflow   string
	Ref        string
	Commit     string
	LoggedAt   time.Time
}

var ErrNoBuildProvenance = errors.New("the registry's attestations hold no SLSA build provenance")

type trust struct {
	rekor   *ecdsa.PublicKey
	rekorID []byte
	roots   *x509.CertPool
	inter   *x509.CertPool
}

func loadTrust() (trust, error) {
	block, _ := pem.Decode([]byte(rekorKeyPEM))
	if block == nil {
		return trust{}, errors.New("the transparency log key doesn't load")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return trust{}, err
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok {
		return trust{}, errors.New("the transparency log key isn't ECDSA")
	}
	id := sha256.Sum256(block.Bytes)
	t := trust{rekor: ec, rekorID: id[:], roots: x509.NewCertPool(), inter: x509.NewCertPool()}
	if !t.roots.AppendCertsFromPEM([]byte(fulcioRootPEM)) || !t.inter.AppendCertsFromPEM([]byte(fulcioIntermediatePEM)) {
		return trust{}, errors.New("the certificate authority doesn't load")
	}
	return t, nil
}

type rawJSON struct {
	RawBytes []byte `json:"rawBytes"`
}

type tlogJSON struct {
	LogIndex string `json:"logIndex"`
	LogID    struct {
		KeyID []byte `json:"keyId"`
	} `json:"logId"`
	KindVersion struct {
		Kind    string `json:"kind"`
		Version string `json:"version"`
	} `json:"kindVersion"`
	IntegratedTime   string `json:"integratedTime"`
	InclusionPromise *struct {
		SignedEntryTimestamp []byte `json:"signedEntryTimestamp"`
	} `json:"inclusionPromise"`
	InclusionProof *struct {
		LogIndex   string   `json:"logIndex"`
		RootHash   []byte   `json:"rootHash"`
		TreeSize   string   `json:"treeSize"`
		Hashes     [][]byte `json:"hashes"`
		Checkpoint struct {
			Envelope string `json:"envelope"`
		} `json:"checkpoint"`
	} `json:"inclusionProof"`
	CanonicalizedBody []byte `json:"canonicalizedBody"`
}

type bundleJSON struct {
	MediaType            string `json:"mediaType"`
	VerificationMaterial struct {
		Certificate          *rawJSON `json:"certificate"`
		X509CertificateChain *struct {
			Certificates []rawJSON `json:"certificates"`
		} `json:"x509CertificateChain"`
		TlogEntries []tlogJSON `json:"tlogEntries"`
	} `json:"verificationMaterial"`
	DSSEEnvelope struct {
		Payload     []byte `json:"payload"`
		PayloadType string `json:"payloadType"`
		Signatures  []struct {
			Sig []byte `json:"sig"`
		} `json:"signatures"`
	} `json:"dsseEnvelope"`
}

type attestationsJSON struct {
	Attestations []struct {
		PredicateType string     `json:"predicateType"`
		Bundle        bundleJSON `json:"bundle"`
	} `json:"attestations"`
}

type statementJSON struct {
	Type          string `json:"_type"`
	PredicateType string `json:"predicateType"`
	Subject       []struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
	Predicate struct {
		BuildDefinition struct {
			ExternalParameters struct {
				Workflow struct {
					Ref        string `json:"ref"`
					Repository string `json:"repository"`
					Path       string `json:"path"`
				} `json:"workflow"`
			} `json:"externalParameters"`
			ResolvedDependencies []struct {
				URI    string            `json:"uri"`
				Digest map[string]string `json:"digest"`
			} `json:"resolvedDependencies"`
		} `json:"buildDefinition"`
	} `json:"predicate"`
}

type rekorBodyJSON struct {
	Kind string `json:"kind"`
	Spec struct {
		PayloadHash struct {
			Algorithm string `json:"algorithm"`
			Value     string `json:"value"`
		} `json:"payloadHash"`
		Signatures []struct {
			Signature string `json:"signature"`
			Verifier  string `json:"verifier"`
		} `json:"signatures"`
	} `json:"spec"`
}

func purl(name, version string) string {
	if scope, rest, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		return "pkg:npm/%40" + url.PathEscape(scope[1:]) + "/" + rest + "@" + version
	}
	return "pkg:npm/" + name + "@" + version
}

func integrityHex(integrity string) (string, error) {
	raw, ok := strings.CutPrefix(integrity, "sha512-")
	if !ok {
		return "", errors.New("the pinned integrity isn't sha512")
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) != sha512.Size {
		return "", errors.New("the pinned integrity isn't a sha512 hash")
	}
	return hex.EncodeToString(b), nil
}

func VerifyAttestations(data []byte, name, version, integrity string, want Source) (Verified, error) {
	var all attestationsJSON
	if err := json.Unmarshal(data, &all); err != nil {
		return Verified{}, errors.New("the registry's attestations aren't JSON this engine reads")
	}
	var found []bundleJSON
	for _, a := range all.Attestations {
		if a.PredicateType == SLSAProvenance {
			found = append(found, a.Bundle)
		}
	}
	if len(found) == 0 {
		return Verified{}, ErrNoBuildProvenance
	}
	if len(found) > 1 {
		return Verified{}, errors.New("the registry has more than one build provenance for this version")
	}
	t, err := loadTrust()
	if err != nil {
		return Verified{}, err
	}
	digest, err := integrityHex(integrity)
	if err != nil {
		return Verified{}, err
	}
	return t.verify(found[0], purl(name, version), digest, want)
}

func (t trust) verify(b bundleJSON, subject, digest string, want Source) (Verified, error) {
	var leafDER []byte
	switch vm := b.VerificationMaterial; {
	case vm.Certificate != nil:
		leafDER = vm.Certificate.RawBytes
	case vm.X509CertificateChain != nil && len(vm.X509CertificateChain.Certificates) > 0:
		leafDER = vm.X509CertificateChain.Certificates[0].RawBytes
	default:
		return Verified{}, errors.New("the provenance carries no signing certificate")
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return Verified{}, errors.New("the provenance's signing certificate doesn't parse")
	}
	env := b.DSSEEnvelope
	if env.PayloadType != inTotoPayload || len(env.Signatures) != 1 || len(env.Payload) == 0 {
		return Verified{}, errors.New("the provenance isn't one signed in-toto statement")
	}
	if len(b.VerificationMaterial.TlogEntries) != 1 {
		return Verified{}, errors.New("the provenance doesn't carry exactly one transparency log entry")
	}
	entry := b.VerificationMaterial.TlogEntries[0]
	logged, err := t.checkEntry(entry)
	if err != nil {
		return Verified{}, err
	}
	if err := checkBody(entry.CanonicalizedBody, env.Payload, env.Signatures[0].Sig, leafDER); err != nil {
		return Verified{}, err
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: t.roots, Intermediates: t.inter, CurrentTime: logged, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning}}); err != nil {
		return Verified{}, errors.New("the signing certificate wasn't issued by Sigstore's certificate authority when the log recorded it")
	}
	key, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return Verified{}, errors.New("the signing certificate's key isn't ECDSA")
	}
	pae := fmt.Sprintf("DSSEv1 %d %s %d ", len(env.PayloadType), env.PayloadType, len(env.Payload))
	h := sha256.Sum256(append([]byte(pae), env.Payload...))
	if !ecdsa.VerifyASN1(key, h[:], env.Signatures[0].Sig) {
		return Verified{}, errors.New("the provenance's signature doesn't match its statement")
	}
	var st statementJSON
	if err := json.Unmarshal(env.Payload, &st); err != nil || st.Type != inTotoStatement || st.PredicateType != SLSAProvenance {
		return Verified{}, errors.New("the signed statement isn't SLSA build provenance")
	}
	if len(st.Subject) != 1 || st.Subject[0].Name != subject || st.Subject[0].Digest["sha512"] != digest {
		return Verified{}, errors.New("the provenance is for another package, version or tarball")
	}
	return checkIdentity(leaf, st, want, logged)
}

func (t trust) checkEntry(e tlogJSON) (time.Time, error) {
	if e.KindVersion.Kind != "dsse" || e.KindVersion.Version != "0.0.1" {
		return time.Time{}, errors.New("the transparency log entry is a kind this engine doesn't check")
	}
	if !bytes.Equal(e.LogID.KeyID, t.rekorID) {
		return time.Time{}, errors.New("the transparency log entry is from another log")
	}
	index, err1 := strconv.ParseInt(e.LogIndex, 10, 64)
	at, err2 := strconv.ParseInt(e.IntegratedTime, 10, 64)
	if err1 != nil || err2 != nil || index < 0 || at <= 0 {
		return time.Time{}, errors.New("the transparency log entry's index or time doesn't parse")
	}
	if e.InclusionPromise == nil {
		return time.Time{}, errors.New("the transparency log entry carries no signed promise")
	}
	set := fmt.Sprintf(`{"body":%q,"integratedTime":%d,"logID":%q,"logIndex":%d}`, base64.StdEncoding.EncodeToString(e.CanonicalizedBody), at, hex.EncodeToString(e.LogID.KeyID), index)
	h := sha256.Sum256([]byte(set))
	if !ecdsa.VerifyASN1(t.rekor, h[:], e.InclusionPromise.SignedEntryTimestamp) {
		return time.Time{}, errors.New("the transparency log's signed promise doesn't match the entry")
	}
	if p := e.InclusionProof; p != nil {
		if err := t.checkProof(e.CanonicalizedBody, p.LogIndex, p.TreeSize, p.RootHash, p.Hashes, p.Checkpoint.Envelope); err != nil {
			return time.Time{}, err
		}
	}
	return time.Unix(at, 0).UTC(), nil
}

func hashChildren(l, r []byte) []byte {
	h := sha256.New()
	h.Write([]byte{1})
	h.Write(l)
	h.Write(r)
	return h.Sum(nil)
}

func (t trust) checkProof(body []byte, indexText, sizeText string, root []byte, proof [][]byte, checkpoint string) error {
	bad := errors.New("the transparency log's inclusion proof doesn't hold")
	index, err1 := strconv.ParseUint(indexText, 10, 64)
	size, err2 := strconv.ParseUint(sizeText, 10, 64)
	if err1 != nil || err2 != nil || index >= size || len(root) != sha256.Size || len(proof) > 64 {
		return bad
	}
	inner := bits.Len64(index ^ (size - 1))
	border := bits.OnesCount64(index >> inner)
	if len(proof) != inner+border {
		return bad
	}
	leaf := sha256.Sum256(append([]byte{0}, body...))
	seed := leaf[:]
	for i, h := range proof[:inner] {
		if len(h) != sha256.Size {
			return bad
		}
		if (index>>i)&1 == 0 {
			seed = hashChildren(seed, h)
		} else {
			seed = hashChildren(h, seed)
		}
	}
	for _, h := range proof[inner:] {
		if len(h) != sha256.Size {
			return bad
		}
		seed = hashChildren(h, seed)
	}
	if !bytes.Equal(seed, root) {
		return bad
	}
	return t.checkCheckpoint(checkpoint, sizeText, root)
}

func (t trust) checkCheckpoint(note, size string, root []byte) error {
	bad := errors.New("the transparency log's signed checkpoint doesn't hold")
	text, sigs, ok := strings.Cut(note, "\n\n")
	if !ok {
		return bad
	}
	text += "\n"
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 3 || !strings.HasPrefix(lines[0], rekorOrigin) || lines[1] != size || lines[2] != base64.StdEncoding.EncodeToString(root) {
		return bad
	}
	h := sha256.Sum256([]byte(text))
	for line := range strings.SplitSeq(sigs, "\n") {
		rest, ok := strings.CutPrefix(line, "— ")
		if !ok {
			continue
		}
		_, b64, ok := strings.Cut(rest, " ")
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) < 5 || !bytes.Equal(raw[:4], t.rekorID[:4]) {
			continue
		}
		if ecdsa.VerifyASN1(t.rekor, h[:], raw[4:]) {
			return nil
		}
	}
	return bad
}

func checkBody(body, payload, sig, leafDER []byte) error {
	bad := errors.New("the transparency log entry doesn't record this signature")
	var b rekorBodyJSON
	if err := json.Unmarshal(body, &b); err != nil || b.Kind != "dsse" {
		return bad
	}
	ph := sha256.Sum256(payload)
	if b.Spec.PayloadHash.Algorithm != "sha256" || b.Spec.PayloadHash.Value != hex.EncodeToString(ph[:]) {
		return bad
	}
	want := base64.StdEncoding.EncodeToString(sig)
	for _, s := range b.Spec.Signatures {
		if s.Signature != want {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(s.Verifier)
		if err != nil {
			return bad
		}
		block, _ := pem.Decode(raw)
		if block != nil && block.Type == "CERTIFICATE" && bytes.Equal(block.Bytes, leafDER) {
			return nil
		}
	}
	return bad
}

func extension(c *x509.Certificate, oid asn1.ObjectIdentifier) string {
	for _, e := range c.Extensions {
		if e.Id.Equal(oid) {
			var s string
			if rest, err := asn1.Unmarshal(e.Value, &s); err == nil && len(rest) == 0 {
				return s
			}
			return ""
		}
	}
	return ""
}

func checkIdentity(leaf *x509.Certificate, st statementJSON, want Source, logged time.Time) (Verified, error) {
	if !want.Set() {
		return Verified{}, errors.New("no source is pinned to check the provenance against")
	}
	signer := want.Repository + "/" + want.Workflow + "@" + want.Ref
	if extension(leaf, oidIssuer) != githubIssuer {
		return Verified{}, errors.New("the provenance wasn't signed by a GitHub Actions workflow")
	}
	if got := extension(leaf, oidSourceRepo); got != want.Repository {
		return Verified{}, fmt.Errorf("the provenance names the repository %q, not the pinned %q", got, want.Repository)
	}
	if got := extension(leaf, oidSourceRef); got != want.Ref {
		return Verified{}, fmt.Errorf("the provenance was built from %q, not the pinned %q", got, want.Ref)
	}
	if got := extension(leaf, oidSigner); got != signer {
		return Verified{}, fmt.Errorf("the provenance was signed by the workflow %q, not the pinned %q", got, signer)
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != signer {
		return Verified{}, errors.New("the signing certificate names another workflow")
	}
	wf := st.Predicate.BuildDefinition.ExternalParameters.Workflow
	if wf.Repository != want.Repository || wf.Path != want.Workflow || wf.Ref != want.Ref {
		return Verified{}, errors.New("the signed statement names another repository, workflow or ref than its certificate")
	}
	commit := extension(leaf, oidSourceSHA)
	for _, d := range st.Predicate.BuildDefinition.ResolvedDependencies {
		if c := d.Digest["gitCommit"]; c != "" && c != commit {
			return Verified{}, errors.New("the signed statement names another commit than its certificate")
		}
	}
	return Verified{Repository: want.Repository, Workflow: want.Workflow, Ref: want.Ref, Commit: commit, LoggedAt: logged}, nil
}
