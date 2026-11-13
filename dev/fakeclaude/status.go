package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func authStatus(args []string) bool {
	if len(args) < 2 || args[0] != "auth" || args[1] != "status" {
		return false
	}
	mode := "subscription"
	if data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".sf-fake-signin")); err == nil {
		mode = strings.TrimSpace(string(data))
	}
	text := slices.Contains(args, "--text")
	loggedIn, method, extra, lines := true, "claude.ai", `,
  "email": "fake.person@example.com",
  "orgId": "00000000-0000-4000-8000-000000000000",
  "orgName": "Example Fake Org",
  "subscriptionType": "max"`, "Login method: Claude Max account\nOrganization: Example Fake Org\nEmail: fake.person@example.com"
	switch mode {
	case "api-key":
		method, extra, lines = "api_key", `,
  "apiKeySource": "ANTHROPIC_API_KEY"`, "API key: ANTHROPIC_API_KEY"
	case "gateway":
		method, extra, lines = "api_key", `,
  "apiKeySource": "apiKeyHelper"`, "API key: apiKeyHelper\nAnthropic base URL: https://gateway.example.test/v1"
	case "none":
		loggedIn, method, extra, lines = false, "none", "", "Not logged in. Run claude auth login to authenticate."
	}
	if text {
		fmt.Println(lines)
	} else {
		fmt.Printf("{\n  \"loggedIn\": %t,\n  \"authMethod\": %q,\n  \"apiProvider\": \"firstParty\",\n  \"analyticsDisabled\": false,\n  \"projectsDirectory\": \"/home/agent/.claude/projects\",\n  \"configDirectory\": \"/home/agent/.claude\",\n  \"loginExpiresAt\": null%s\n}\n", loggedIn, method, extra)
	}
	if !loggedIn {
		os.Exit(1)
	}
	os.Exit(0)
	return true
}
