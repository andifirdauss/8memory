package refs

import (
	"path/filepath"
	"strings"
	"testing"
)

const secretRoot = "secret-root"

var aliases = map[string]string{"backup": secretRoot}

func TestResolveTranslatesPerMachine(t *testing.T) {
	const ref = "backup://project/2026-11-09/database.sql"
	laptop, err := Resolve(ref, map[string]string{"backup": filepath.Join("laptop", "Backup")})
	if err != nil {
		t.Fatal(err)
	}
	vps, err := Resolve(ref, map[string]string{"backup": filepath.Join("srv", "backup")})
	if err != nil {
		t.Fatal(err)
	}
	if laptop != filepath.Join("laptop", "Backup", "project", "2026-11-09", "database.sql") ||
		vps != filepath.Join("srv", "backup", "project", "2026-11-09", "database.sql") {
		t.Fatalf("%s, %s", laptop, vps)
	}
}

func TestResolveKeepsS3(t *testing.T) {
	const ref = "s3://bucket/project/database.sql"
	if got, err := Resolve(ref, nil); err != nil || got != ref {
		t.Fatalf("%s, %v", got, err)
	}
}

func TestValidateRejectsUnsafeRefs(t *testing.T) {
	for _, ref := range []string{
		"",
		`D:\Backup\database.sql`,
		"D:/Backup/database.sql",
		"/srv/backup/database.sql",
		"file:///etc/passwd",
		"https://example.com/x.sql",
		"d://Backup/x.sql",
		"BACKUP://x.sql",
		"other://x.sql",
		"backup://",
		"backup://../secret",
		"backup://a/../../secret",
		"backup://a/./b",
		"backup://a//b",
		`backup://a\..\b`,
		"backup://C:/secret",
		"backup://a/file.sql:stream",
		"backup://a\x00b",
		"s3://bucketonly",
		"backup://" + strings.Repeat("a", maxRefLength),
	} {
		if err := Validate(ref, aliases); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
}

func TestErrorsNeverExposeTheRealPath(t *testing.T) {
	for _, ref := range []string{"other://x", "D:/x", "backup://../x"} {
		if err := Validate(ref, aliases); err == nil || strings.Contains(err.Error(), secretRoot) {
			t.Errorf("%q: %v", ref, err)
		}
	}
}

func TestValidateAlias(t *testing.T) {
	for _, name := range []string{"backup", "archive-2", "nas"} {
		if err := ValidateAlias(name); err != nil {
			t.Error(err)
		}
	}
	for _, name := range []string{"", "d", "Backup", "a_b", "s3", "file", "http", "https"} {
		if ValidateAlias(name) == nil {
			t.Errorf("accepted %q", name)
		}
	}
}
