package config

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadDefaultsToSQLiteNextToConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Backend != SQLite || c.Path != filepath.Join(filepath.Dir(path), "memory.db") {
		t.Fatalf("%+v", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{Backend: Postgres, Host: "db", Port: 5432, User: "u", Password: "p", Database: "d", Options: "sslmode=require", Aliases: map[string]string{"backup": "/srv/backup"}}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestLoadResolvesRelativeSQLitePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"backend":"sqlite","path":"data.db"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.Path != filepath.Join(filepath.Dir(path), "data.db") {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestDSNPreservesSpecialPasswordCharacters(t *testing.T) {
	const password = "p@ss:w/rd?#%&"
	for _, backend := range []Backend{Postgres, SQLServer, Oracle} {
		dsn, err := Config{Backend: backend, Host: "h", Port: 1, User: "u", Password: password, Database: "d"}.DSN()
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := u.User.Password(); got != password {
			t.Errorf("%s: password %q", backend, got)
		}
	}
}

func TestDescribeHidesPassword(t *testing.T) {
	got := Config{Backend: Oracle, Host: "h", Port: 1521, User: "u", Password: "secret", Database: "svc"}.Describe()
	if got != "oracle u@h:1521/svc" {
		t.Fatal(got)
	}
}

func TestValidateRejectsIncompleteConfig(t *testing.T) {
	for _, c := range []Config{
		{Backend: "mongo"},
		{Backend: SQLite},
		{Backend: Postgres, Port: 5432, User: "u", Database: "d"},
		{Backend: Postgres, Host: "h", Port: 0, User: "u", Database: "d"},
		{Backend: Postgres, Host: "h", Port: 5432, Database: "d"},
		{Backend: Postgres, Host: "h", Port: 5432, User: "u"},
		{Backend: Postgres, Host: "h", Port: 5432, User: "u", Database: "d", Options: "a=%zz"},
	} {
		if c.Validate() == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestAliasesOnlyConfigKeepsDefaultStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"aliases":{"backup":"/srv/backup"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.Backend != SQLite || c.Path != filepath.Join(filepath.Dir(path), "memory.db") || c.Aliases["backup"] != "/srv/backup" {
		t.Fatalf("%+v, %v", c, err)
	}
}

func TestInvalidAliasRejected(t *testing.T) {
	base := Config{Backend: SQLite, Path: "x"}
	for _, aliases := range []map[string]string{{"s3": "/a"}, {"d": "/a"}, {"backup": " "}} {
		base.Aliases = aliases
		if base.Validate() == nil {
			t.Errorf("accepted %v", aliases)
		}
	}
}
