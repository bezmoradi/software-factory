package github

import "net/http"

const tokenExpiryHeader = "GitHub-Authentication-Token-Expiration"

func (a API) noteExpiry(host string, resp *http.Response) {
	if a.Expiry == nil || resp.StatusCode == http.StatusUnauthorized {
		return
	}
	a.Expiry(bare(host), resp.Header.Get(tokenExpiryHeader))
}
