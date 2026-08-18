package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// compose.yaml IS the quickstart — a stranger curls that one file and runs
// `docker compose up -d` — so a mistake in it breaks the install for everybody
// who is not us, and no Go test would otherwise notice.
//
// The guard below exists because that happened. See docs/decisions.md D45.

// requiredVar matches compose's mandatory-variable form, `${NAME:?message}` or
// `${NAME?message}`, which makes interpolation fail when NAME is unset.
var requiredVar = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:?\?`)

// TestNoProfiledServiceUsesARequiredVariable pins D45.
//
// Compose interpolates the WHOLE file before it filters profiles, so a
// `${VAR:?...}` inside an opt-in service is not opt-in: it fails `docker compose
// up -d` for everyone, including the people who never asked for that service.
// T80 shipped `${UPDATER_TOKEN:?...}` in the `updater` profile and the quickstart
// stopped working — measured from a clean directory, `docker compose up -d`
// answered `required variable UPDATER_TOKEN is missing a value` and started
// nothing at all.
//
// A profiled service's mandatory values belong to the service, which can refuse
// when it actually runs. watchtower does exactly that: with an empty token it
// logs `api token is empty or has not been set. exiting` and exits 1 before the
// HTTP API listens, so nothing is weakened by taking the `:?` out here.
func TestNoProfiledServiceUsesARequiredVariable(t *testing.T) {
	path := filepath.Join("..", "..", "compose.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}

	// A hand-rolled walk rather than a YAML dependency: this test asserts on a
	// file the build does not otherwise parse, and adding a parser to the module
	// for one assertion is a worse trade than tracking indentation.
	var (
		service    string
		profiled   = map[string]bool{}
		offenders  []string
		lineOf     = map[string]int{}
		inServices bool
	)
	for i, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))

		switch {
		case indent == 0:
			inServices = strings.HasPrefix(line, "services:")
			service = ""
		case inServices && indent == 2 && strings.HasSuffix(trimmed, ":"):
			service = strings.TrimSuffix(trimmed, ":")
		}
		if service == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "profiles:") {
			profiled[service] = true
		}
		if requiredVar.MatchString(line) {
			offenders = append(offenders, service)
			lineOf[service] = i + 1
		}
	}

	for _, s := range offenders {
		if profiled[s] {
			t.Errorf("compose.yaml:%d: service %q is behind a profile and uses a required "+
				"${VAR:?...}. Compose interpolates before it filters profiles, so this fails "+
				"`docker compose up -d` for everyone who did not ask for %q. Let the service "+
				"refuse instead (docs/decisions.md D45).", lineOf[s], s, s)
		}
	}
}

// TestTheQuickstartServiceIsTheOnlyDefaultOne keeps the other half of the same
// promise: `docker compose up -d` with no profile starts curator and nothing
// else. A media server or an updater arriving uninvited is what D34 refused and
// what the profiles exist for, and a dropped `profiles:` line has no symptom
// until somebody's box grows a container they did not choose.
func TestTheQuickstartServiceIsTheOnlyDefaultOne(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}

	var (
		service    string
		services   []string
		profiled   = map[string]bool{}
		inServices bool
	)
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))

		switch {
		case indent == 0:
			inServices = strings.HasPrefix(line, "services:")
			service = ""
		case inServices && indent == 2 && strings.HasSuffix(trimmed, ":"):
			service = strings.TrimSuffix(trimmed, ":")
			services = append(services, service)
		}
		if service != "" && strings.HasPrefix(trimmed, "profiles:") {
			profiled[service] = true
		}
	}

	if len(services) < 2 {
		t.Fatalf("parsed %d services from compose.yaml, which means the walk is wrong, "+
			"not that the file is: %v", len(services), services)
	}

	var byDefault []string
	for _, s := range services {
		if !profiled[s] {
			byDefault = append(byDefault, s)
		}
	}
	if len(byDefault) != 1 || byDefault[0] != "curator" {
		t.Errorf("services started by a bare `docker compose up -d` = %v, want [curator]. "+
			"Everything else is opt-in (docs/decisions.md D34, D44).", byDefault)
	}
}
