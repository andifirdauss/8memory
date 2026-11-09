package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"8memory/internal/config"
	"8memory/internal/store"
)

const backupRoot = "folder-rahasia-backup"

func connectWithBackup(t *testing.T) (context.Context, *mcp.ClientSession) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, config.Config{Backend: config.SQLite, Path: filepath.Join(t.TempDir(), "m.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	serverSide, clientSide := mcp.NewInMemoryTransports()
	if _, err := New(s, map[string]string{"backup": backupRoot}).Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return ctx, session
}

func toolError(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if !result.IsError {
		t.Fatal("want a tool error")
	}
	return result.Content[0].(*mcp.TextContent).Text
}

func writeArgs(content string, files ...map[string]any) map[string]any {
	args := map[string]any{"change": "add", "feature": "Backup", "content": content, "project": "demo", "branch": "main"}
	if len(files) > 0 {
		args["files"] = files
	}
	return args
}

func TestContentGuard(t *testing.T) {
	ctx, session := connectWithBackup(t)
	inserts := strings.Repeat("INSERT INTO t VALUES (1);\n", dumpInsertLimit)
	cases := map[string]string{
		"over limit":    strings.Repeat("a", maxContentBytes+1),
		"postgres dump": "--\n-- PostgreSQL database dump\n--\n",
		"mysql dump":    "-- MySQL dump 10.13\n",
		"many inserts":  inserts,
		"lowercase":     strings.ToLower(inserts),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			message := toolError(t, callText(t, ctx, session, "memory_write", writeArgs(content)))
			for _, want := range []string{"outside the project", "files parameter", "backup"} {
				if !strings.Contains(message, want) {
					t.Fatalf("message lacks %q: %s", want, message)
				}
			}
			if strings.Contains(message, backupRoot) {
				t.Fatalf("real path exposed: %s", message)
			}
		})
	}
}

func TestContentGuardAllowsNormalNotes(t *testing.T) {
	ctx, session := connectWithBackup(t)
	for i, content := range []string{
		strings.Repeat("a", maxContentBytes),
		strings.Repeat("INSERT INTO t VALUES (1);\n", dumpInsertLimit-1),
		"Menambahkan pengecekan INSERT INTO pada migrasi",
	} {
		args := writeArgs(content)
		args["project"] = fmt.Sprintf("demo-%d", i)
		mustCall(t, ctx, session, "memory_write", args)
	}
}

func TestFilesAreStoredAndFilledWithHost(t *testing.T) {
	ctx, session := connectWithBackup(t)
	saved := decode[store.Memory](t, mustCall(t, ctx, session, "memory_write", writeArgs("Backup sebelum migrasi",
		map[string]any{"ref": "backup://demo/2026-11-09/database.sql", "size": 1048576, "kind": "postgres-dump", "restore_hint": "psql -f database.sql"},
		map[string]any{"ref": "s3://arsip/demo/uploads.tar", "host": "vps-1"},
	)))
	if len(saved.Files) != 2 {
		t.Fatalf("%+v", saved)
	}
	hostname, _ := os.Hostname()
	first, second := saved.Files[0], saved.Files[1]
	if first.Size != 1048576 || first.Kind != "postgres-dump" || first.RestoreHint != "psql -f database.sql" || first.Host != hostname {
		t.Fatalf("%+v", first)
	}
	if second.Host != "vps-1" {
		t.Fatalf("%+v", second)
	}

	found := decode[searchOutput](t, mustCall(t, ctx, session, "memory_search", map[string]any{"query": "migrasi", "project": "demo", "branch": "*"}))
	if len(found.Memories) != 1 || len(found.Memories[0].Files) != 2 || found.Memories[0].Files[0].Ref != first.Ref {
		t.Fatalf("%+v", found)
	}
	raw := found.Memories[0].Files[0].Ref + first.RestoreHint
	if strings.Contains(raw, backupRoot) {
		t.Fatal(raw)
	}
}

func TestInvalidFilesRejected(t *testing.T) {
	ctx, session := connectWithBackup(t)
	for _, file := range []map[string]any{
		{"ref": `D:\Backup\database.sql`},
		{"ref": "/srv/backup/database.sql"},
		{"ref": "lain://demo/database.sql"},
		{"ref": "backup://../rahasia"},
		{"ref": "backup://demo/a.sql", "size": -1},
		{"ref": "backup://demo/a.sql", "restore_hint": strings.Repeat("x", maxHintRunes+1)},
	} {
		message := toolError(t, callText(t, ctx, session, "memory_write", writeArgs("isi", file)))
		if strings.Contains(message, backupRoot) {
			t.Fatalf("real path exposed: %s", message)
		}
	}
	tooMany := make([]map[string]any, maxFiles+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"ref": fmt.Sprintf("backup://demo/%d.sql", i)}
	}
	toolError(t, callText(t, ctx, session, "memory_write", writeArgs("isi", tooMany...)))
}

func TestSameNoteWithDifferentFileIsKept(t *testing.T) {
	ctx, session := connectWithBackup(t)
	first := decode[store.Memory](t, mustCall(t, ctx, session, "memory_write", writeArgs("Backup harian", map[string]any{"ref": "backup://demo/senin.sql"})))
	second := decode[store.Memory](t, mustCall(t, ctx, session, "memory_write", writeArgs("Backup harian", map[string]any{"ref": "backup://demo/selasa.sql"})))
	if first.ID == second.ID || second.Files[0].Ref != "backup://demo/selasa.sql" {
		t.Fatalf("second backup was dropped: %+v %+v", first, second)
	}
}
