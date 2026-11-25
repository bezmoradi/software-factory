package sts

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bezmoradi/software-factory/internal/core/session"
)

const (
	APIVersion  = "2011-06-15"
	service     = "sts"
	algorithm   = "AWS4-HMAC-SHA256"
	formType    = "application/x-www-form-urlencoded; charset=utf-8"
	amzDate     = "20060102T150405Z"
	shortDate   = "20060102"
	maxAnswer   = 64 << 10
	callFor     = 15 * time.Second
	maxTokenLen = 8 << 10
)

var (
	regionName = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]{1,2}$`)
	errorCode  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.]{0,63}$`)
	tempKeyID  = regexp.MustCompile(`^ASIA[A-Z0-9]{16}$`)
	secretKey  = regexp.MustCompile(`^[A-Za-z0-9/+=]{40}$`)
	tokenChars = regexp.MustCompile(`^[A-Za-z0-9/+=]+$`)
)

type Copier struct {
	Client *http.Client
	Now    func() time.Time
}

var _ session.AWSCopies = Copier{}

func New() Copier {
	return Copier{Client: &http.Client{
		Timeout:       callFor,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       time.Minute,
		},
	}, Now: time.Now}
}

func host(region string) string {
	if strings.HasPrefix(region, "cn-") {
		return "sts." + region + ".amazonaws.com.cn"
	}
	return "sts." + region + ".amazonaws.com"
}

func hashHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func mac(key []byte, text string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(text))
	return h.Sum(nil)
}

func signingKey(secret, day, region, svc string) []byte {
	return mac(mac(mac(mac([]byte("AWS4"+secret), day), region), svc), "aws4_request")
}

func canonicalRequest(method, path string, headers map[string]string, body []byte) (string, string) {
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, strings.ToLower(k))
	}
	sort.Strings(names)
	values := map[string]string{}
	for k, v := range headers {
		values[strings.ToLower(k)] = strings.Join(strings.Fields(v), " ")
	}
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	return method + "\n" + path + "\n\n" + b.String() + "\n" + signed + "\n" + hashHex(body), signed
}

func authorization(keyID, secret, region, svc string, at time.Time, canonical, signed string) string {
	at = at.UTC()
	scope := at.Format(shortDate) + "/" + region + "/" + svc + "/aws4_request"
	toSign := algorithm + "\n" + at.Format(amzDate) + "\n" + scope + "\n" + hashHex([]byte(canonical))
	sig := hex.EncodeToString(mac(signingKey(secret, at.Format(shortDate), region, svc), toSign))
	return algorithm + " Credential=" + keyID + "/" + scope + ", SignedHeaders=" + signed + ", Signature=" + sig
}

type credentialsXML struct {
	AccessKeyID     string `xml:"AccessKeyId"`
	SecretAccessKey string `xml:"SecretAccessKey"`
	SessionToken    string `xml:"SessionToken"`
	Expiration      string `xml:"Expiration"`
}

type answerXML struct {
	Assumed credentialsXML `xml:"AssumeRoleResult>Credentials"`
	Session credentialsXML `xml:"GetSessionTokenResult>Credentials"`
	Code    string         `xml:"Error>Code"`
}

var errMalformed = errors.New("sts: the answer isn't a set of temporary credentials")

func (c Copier) Copy(ctx context.Context, q session.AWSCopyAsk) (session.AWSCopy, error) {
	if !regionName.MatchString(q.Region) {
		return session.AWSCopy{}, errors.New("sts: not a region")
	}
	form := url.Values{"Version": {APIVersion}, "DurationSeconds": {strconv.Itoa(int(q.Duration / time.Second))}}
	if q.Role != "" {
		form.Set("Action", "AssumeRole")
		form.Set("RoleArn", q.Role)
		form.Set("RoleSessionName", q.Name)
	} else {
		form.Set("Action", "GetSessionToken")
	}
	body := []byte(form.Encode())
	addr := host(q.Region)
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	at := now().UTC()
	headers := map[string]string{"Content-Type": formType, "Host": addr, "X-Amz-Date": at.Format(amzDate)}
	canonical, signed := canonicalRequest(http.MethodPost, "/", headers, body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+"/", bytes.NewReader(body))
	if err != nil {
		return session.AWSCopy{}, err
	}
	req.Header.Set("Content-Type", formType)
	req.Header.Set("X-Amz-Date", at.Format(amzDate))
	req.Header.Set("Authorization", authorization(q.KeyID, q.SecretKey, q.Region, service, at, canonical, signed))
	req.Header.Set("Accept", "text/xml")
	resp, err := c.Client.Do(req)
	if err != nil {
		return session.AWSCopy{}, errors.New("sts: no answer")
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil || len(data) > maxAnswer {
		return session.AWSCopy{}, errors.New("sts: the answer didn't arrive whole")
	}
	var a answerXML
	parsed := xml.Unmarshal(data, &a) == nil
	if resp.StatusCode != http.StatusOK {
		code := ""
		if parsed && errorCode.MatchString(a.Code) {
			code = a.Code
		}
		return session.AWSCopy{}, session.AWSRefused{Code: code, Status: resp.StatusCode}
	}
	got := a.Session
	if q.Role != "" {
		got = a.Assumed
	}
	expires, err := time.Parse(time.RFC3339, got.Expiration)
	if !parsed || err != nil || !tempKeyID.MatchString(got.AccessKeyID) || !secretKey.MatchString(got.SecretAccessKey) ||
		len(got.SessionToken) > maxTokenLen || !tokenChars.MatchString(got.SessionToken) || !expires.After(at) {
		return session.AWSCopy{}, errMalformed
	}
	return session.AWSCopy{KeyID: got.AccessKeyID, SecretKey: got.SecretAccessKey, Token: got.SessionToken, Expires: expires}, nil
}
