// Package config loads and saves the storage settings shared by the MCP
// server and the TUI.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"8memory/internal/refs"
)

// Backend names a supported storage engine.
type Backend string

const (
	SQLite    Backend = "sqlite"
	Postgres  Backend = "postgres"
	SQLServer Backend = "sqlserver"
	Oracle    Backend = "oracle"
)

// Config selects a backend and how to reach it. Path applies to SQLite only;
// the network fields apply to every other backend. For Oracle, Database holds
// the service name.
type Config struct {
	Backend  Backend `json:"backend"`
	Path     string  `json:"path,omitempty"`
	Host     string  `json:"host,omitempty"`
	Port     int     `json:"port,omitempty"`
	User     string  `json:"user,omitempty"`
	Password string  `json:"password,omitempty"`
	Database string  `json:"database,omitempty"`
	Options  string  `json:"options,omitempty"` // URL query string, e.g. sslmode=require
	// Aliases maps a ref scheme to a directory on this machine, for example
	// backup -> D:\\Backup. Each machine keeps its own mapping.
	Aliases map[string]string `json:"aliases,omitempty"`
	// PasteLines and PasteBytes set when a paste in ToCLI becomes an
	// attachment. Zero uses the defaults.
	PasteLines int `json:"paste_lines,omitempty"`
	PasteBytes int `json:"paste_bytes,omitempty"`
}

// DefaultPath returns ~/.8memory/config.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".8memory", "config.json"), nil
}

// Default is the zero-setup configuration: SQLite next to the config file.
func Default(configPath string) Config {
	return Config{Backend: SQLite, Path: filepath.Join(filepath.Dir(configPath), "memory.db")}
}

// Load reads the config file, returning Default when it does not exist yet.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default(path), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	// A hand-written file may set only aliases and keep the default storage.
	if c.Backend == "" {
		d := Default(path)
		c.Backend, c.Path = d.Backend, d.Path
	}
	// MCP clients start the server from arbitrary directories, so a relative
	// SQLite path must not depend on the working directory.
	if c.Backend == SQLite && c.Path != "" && !filepath.IsAbs(c.Path) {
		c.Path = filepath.Join(filepath.Dir(path), c.Path)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", path, err)
	}
	return c, nil
}

// Save writes the config atomically, so a crash cannot leave a truncated file
// that would stop every MCP client from starting.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// Validate reports the first missing or malformed field.
func (c Config) Validate() error {
	if c.PasteLines < 0 || c.PasteBytes < 0 {
		return errors.New("paste_lines and paste_bytes must not be negative")
	}
	for name, dir := range c.Aliases {
		if err := refs.ValidateAlias(name); err != nil {
			return err
		}
		if strings.TrimSpace(dir) == "" {
			return fmt.Errorf("alias %q has no directory", name)
		}
	}
	switch c.Backend {
	case SQLite:
		if strings.TrimSpace(c.Path) == "" {
			return errors.New("path is required")
		}
		return nil
	case Postgres, SQLServer, Oracle:
	default:
		return fmt.Errorf("unknown backend %q", c.Backend)
	}
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("host is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.User) == "" {
		return errors.New("user is required")
	}
	if strings.TrimSpace(c.Database) == "" {
		return errors.New("database is required")
	}
	if _, err := url.ParseQuery(c.Options); err != nil {
		return fmt.Errorf("options must be a query string (a=b&c=d): %w", err)
	}
	return nil
}

// Describe identifies the storage target without exposing the password. Two
// configs with the same description point at the same data.
func (c Config) Describe() string {
	if c.Backend == SQLite {
		return fmt.Sprintf("sqlite %s", filepath.Clean(c.Path))
	}
	return fmt.Sprintf("%s %s@%s/%s", c.Backend, c.User, net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), c.Database)
}

// DSN builds the driver connection string. Credentials go through net/url so
// characters such as @ : / in a password stay intact.
func (c Config) DSN() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if c.Backend == SQLite {
		return c.Path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", nil
	}
	query, err := url.ParseQuery(c.Options)
	if err != nil {
		return "", err
	}
	u := url.URL{
		Scheme: string(c.Backend),
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
	}
	if c.Backend == SQLServer {
		query.Set("database", c.Database)
	} else {
		u.Path = "/" + c.Database
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}
