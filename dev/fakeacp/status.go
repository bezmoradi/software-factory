package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
  "subscriptionType": "max",
  "planTier": "max_20x"`, "Login method: Claude Max account\nOrganization: Example Fake Org\nEmail: fake.person@example.com"
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
		fmt.Printf("{\n  \"loggedIn\": %t,\n  \"authMethod\": %q,\n  \"apiProvider\": \"firstParty\",\n  \"analyticsDisabled\": false,\n  \"projectsDirectory\": \"/home/agent/.claude/projects\",\n  \"configDirectory\": \"/home/agent/.claude\",\n  \"loginExpiresAt\": %s%s\n}\n", loggedIn, method, loginEnds(method), extra)
	}
	if !loggedIn {
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

func loginEnds(method string) string {
	data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".sf-fake-login-ends"))
	if err != nil || method != "claude.ai" {
		return "null"
	}
	return strconv.Quote(strings.TrimSpace(string(data)))
}

func authLogin(args []string) bool {
	if len(args) < 2 || args[0] != "auth" || args[1] != "login" {
		return false
	}
	fmt.Println("Opening the browser to sign in. If it doesn't open, visit: https://claude.com/cai/oauth/authorize?code=true&client_id=sf-fake&state=sf-fake")
	fmt.Print("Paste code here if prompted > ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	code := strings.TrimSpace(line)
	if err != nil || code == "" || code == "bad" {
		fmt.Println("Login failed: invalid code.")
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), ".sf-fake-signin"), []byte("subscription\n"), 0o600); err != nil {
		fmt.Println("Login failed: the sign-in couldn't be saved.")
		os.Exit(1)
	}
	fmt.Println("Login successful.")
	os.Exit(0)
	return true
}
