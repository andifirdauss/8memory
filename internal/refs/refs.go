// Package refs validates and resolves references to files kept outside
// memory, such as backup://project/2026-11-09/database.sql or s3://bucket/key.
// An alias stands for a directory that differs per machine, so a memory stays
// valid everywhere and the AI never sees the real path.
package refs

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// RemoteScheme is the object-store scheme accepted without an alias.
const RemoteScheme = "s3"

const maxRefLength = 1000

var (
	refPattern   = regexp.MustCompile(`^([a-z][a-z0-9-]+)://(.+)$`)
	aliasPattern = regexp.MustCompile(`^[a-z][a-z0-9-]+$`)
	reserved     = []string{RemoteScheme, "file", "http", "https", "ftp"}
)

// ValidateAlias checks that name can be used as a ref scheme. Two characters
// is the minimum because a single letter would be read as a drive letter.
func ValidateAlias(name string) error {
	if !aliasPattern.MatchString(name) {
		return fmt.Errorf("alias %q must be lowercase letters, digits or -, at least 2 characters", name)
	}
	if slices.Contains(reserved, name) {
		return fmt.Errorf("alias %q is reserved", name)
	}
	return nil
}

// Schemes lists every scheme a ref may use on this machine.
func Schemes(aliases map[string]string) []string {
	out := []string{RemoteScheme}
	for name := range aliases {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// parse splits a ref and rejects anything that could reach outside the
// alias's directory. Errors name only aliases, never their paths.
func parse(ref string, aliases map[string]string) (scheme string, segments []string, err error) {
	if len(ref) > maxRefLength {
		return "", nil, fmt.Errorf("ref is longer than %d characters", maxRefLength)
	}
	match := refPattern.FindStringSubmatch(ref)
	if match == nil {
		return "", nil, fmt.Errorf("ref %q must look like alias://path/to/file, not a local path; available schemes: %s",
			ref, strings.Join(Schemes(aliases), ", "))
	}
	scheme = match[1]
	_, isAlias := aliases[scheme]
	if scheme != RemoteScheme && !isAlias {
		return "", nil, fmt.Errorf("unknown ref scheme %q; available schemes: %s", scheme, strings.Join(Schemes(aliases), ", "))
	}
	segments = strings.Split(match[2], "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, `\:`) || hasControl(segment) {
			return "", nil, fmt.Errorf("ref %q has an invalid path segment %q", ref, segment)
		}
	}
	if scheme == RemoteScheme && len(segments) < 2 {
		return "", nil, errors.New("an s3 ref needs a bucket and a key: s3://bucket/key")
	}
	return scheme, segments, nil
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// Validate reports whether ref is usable on this machine.
func Validate(ref string, aliases map[string]string) error {
	_, _, err := parse(ref, aliases)
	return err
}

// Resolve translates a ref to its location on this machine. An s3 ref is
// already global and is returned unchanged.
func Resolve(ref string, aliases map[string]string) (string, error) {
	scheme, segments, err := parse(ref, aliases)
	if err != nil {
		return "", err
	}
	if scheme == RemoteScheme {
		return ref, nil
	}
	return filepath.Join(append([]string{aliases[scheme]}, segments...)...), nil
}
