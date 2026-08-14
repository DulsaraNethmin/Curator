package settings

import (
	"fmt"
	"os"
	"strings"
)

// Source is where a value came from.
type Source string

const (
	SourceEnv     Source = "env"
	SourceStored  Source = "stored"
	SourceDefault Source = "default"
)

// Resolution is every setting's winning value, and where it won from.
type Resolution struct {
	// Values holds only the settings that have one, keyed by setting key. A key
	// that is absent has no configured value anywhere and takes the default
	// internal/config holds for it.
	Values map[string]string

	// Sources has an entry for every setting in the registry, including the
	// ones with no value — SourceDefault is a fact a screen needs to render.
	Sources map[string]Source
}

// Resolve applies precedence: the environment wins, the store fills the rest,
// and whatever is left is internal/config's default.
//
//	environment variable, if set   →   stored setting, if present   →   default
//
// The environment winning is what keeps every existing deployment working and
// `docker run -e` honest. The property that actually decided the order, though,
// is recovery: a bad stored value is always beatable by one -e, so there is no
// rescue mode to build, no offline editor, and no support answer that begins
// "open the database". D25's lockout escape hatch is this rule and nothing else.
//
// Its one sharp edge is that a stored value the environment shadows is silently
// ignored, which is why Sources exists and why the settings screen renders a
// shadowed field read-only instead of letting somebody type into it.
func Resolve(stored map[string]string) (Resolution, error) {
	res := Resolution{
		Values:  make(map[string]string, len(registry)),
		Sources: make(map[string]Source, len(registry)),
	}

	for _, s := range registry {
		res.Sources[s.Key] = SourceDefault

		if v := strings.TrimSpace(os.Getenv(s.Env)); v != "" {
			res.Values[s.Key] = v
			res.Sources[s.Key] = SourceEnv
			continue
		}

		if s.FileEnv != "" {
			path := strings.TrimSpace(os.Getenv(s.FileEnv))
			if path != "" {
				body, err := os.ReadFile(path)
				if err != nil {
					// Fatal rather than a silent fall-through to the stored
					// value: a mandatory tunnel that quietly becomes a
					// different tunnel because of a typo in a path is the
					// failure D27's whole design is trying not to have.
					return Resolution{}, fmt.Errorf("%s %s: %w", s.FileEnv, path, err)
				}
				res.Values[s.Key] = string(body)
				res.Sources[s.Key] = SourceEnv
				continue
			}
		}

		if v, ok := stored[s.Key]; ok && v != "" {
			res.Values[s.Key] = v
			res.Sources[s.Key] = SourceStored
		}
	}

	return res, nil
}

// Secrets returns the values that must never appear in a log line, for
// logs.NewBuffer to scrub on the way in.
//
// It reads them off the resolution rather than off *config.Config because the
// registry already knows which settings are secrets, and a list maintained by
// hand in cmd/curator is one that gains a member and not a line — which is
// exactly what happened to the tunnel's keys in phase 6
// (docs/tasks/T37-tunnel.md said to hand them over; nothing did).
func (r Resolution) Secrets() []string {
	var out []string
	for _, s := range registry {
		if !s.Secret {
			continue
		}
		v := r.Values[s.Key]
		if v == "" {
			continue
		}
		if s.expand != nil {
			out = append(out, s.expand(v)...)
			continue
		}
		out = append(out, v)
	}
	return out
}
