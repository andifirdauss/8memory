# 8memory

Memori dan journey bersama untuk AI coding tool. Claude Code, Codex, Cursor, dan Antigravity menulis dan membaca data yang sama lewat MCP, sehingga konteks proyek tersimpan di luar AI.

| | Journey | Memori |
| --- | --- | --- |
| Isi | Tanggal, waktu, judul | Fitur apa yang ditambah, dikurangi, diubah, atau hanya diperiksa |
| Jumlah | Satu node UUID per prompt | Satu catatan per fitur, tertaut ke node |
| Dipakai untuk | Linimasa murah token: 1 hari, 7 hari, 1 bulan | Detail keputusan saat dibutuhkan |

Proyek dan branch dideteksi otomatis dari git. Memori dan journey terpisah per branch, dan branch lain tetap bisa dibaca dengan `branch: "*"`.

## Build

```sh
go build -trimpath -ldflags "-s -w" -o 8memory.exe ./cmd/8memory
```

## TUI

```sh
8memory tui
```

- **Journey:** linimasa prompt per 1 hari, 7 hari, 1 bulan, atau semua (`p`). Dikelompokkan menurut proyek/branch, proyek, branch, hari, atau agent (`g`). Enter membuka memori milik node itu.
- **Memori:** filter periode (`p`), grup (`g`), jenis perubahan (`c`), dan pencarian (`/`). Enter membuka isi lengkap.
- **Penyimpanan:** ganti backend dan salin data yang sudah ada. Penyalinan aman diulang. Restart AI client setelah mengganti backend.

| Backend | Pencarian memori |
| --- | --- |
| SQLite (default, `~/.8memory/memory.db`) | FTS5, diurutkan menurut relevansi |
| PostgreSQL | `tsvector` + GIN, diurutkan menurut relevansi |
| SQL Server | `LIKE`, terbaru dulu, memindai seluruh tabel |
| Oracle | `LIKE`, terbaru dulu, memindai seluruh tabel |

Pengaturan disimpan di `~/.8memory/config.json`. Password tersimpan sebagai teks biasa di file itu, jadi jaga akses ke folder tersebut.

## Mendaftarkan ke AI client

Claude Code:

```sh
claude mcp add --scope user 8memory -- "D:\path\ke\8memory.exe" serve
```

Codex (`~/.codex/config.toml`):

```toml
[mcp_servers.8memory]
command = 'D:\path\ke\8memory.exe'
args = ["serve"]
```

Cursor dan Antigravity (file konfigurasi MCP masing-masing):

```json
{
  "mcpServers": {
    "8memory": { "command": "D:\\path\\ke\\8memory.exe", "args": ["serve"] }
  }
}
```

### Node otomatis di Claude Code

Tambahkan hook ini ke `~/.claude/settings.json` supaya setiap prompt pasti menjadi node, tanpa bergantung pada model dan tanpa memakai token. Judulnya diambil dari baris pertama prompt.

```json
{
  "hooks": {
    "UserPromptSubmit": [
      { "hooks": [{ "type": "command", "command": "\"D:\\path\\ke\\8memory.exe\" hook --agent claude" }] }
    ]
  }
}
```

Di client tanpa hook, model membuat node sendiri dengan `journey_mark` sesuai instruksi server.

### Instruksi untuk AI

Tambahkan ini ke `CLAUDE.md` atau `AGENTS.md`:

```md
Di awal sesi, panggil journey_list dan memory_search dengan cwd proyek ini.
Setelah prompt selesai, panggil memory_write sekali per fitur, dengan change add, remove, modify, atau inspect, dan node_id prompt ini. Isi tags dengan sinonim Indonesia dan Inggris.
```

## File besar

`memory_write` menolak `content` di atas 16 KB dan teks yang jelas berupa dump, misalnya berisi `-- PostgreSQL database dump` atau 100 lebih `INSERT INTO`. File seperti itu disimpan di luar proyek, lalu memori hanya menyimpan ringkasan dan referensinya lewat parameter `files`:

```json
{
  "change": "inspect",
  "feature": "Backup database",
  "content": "Backup penuh sebelum migrasi skema",
  "files": [{
    "ref": "backup://namaproyek/2026-11-09/database.sql",
    "size": 5368709120,
    "kind": "postgres-dump",
    "restore_hint": "psql -d app -f database.sql"
  }]
}
```

`ref` memakai alias atau langsung `s3://bucket/key`. Path lokal, `..`, dan alias yang belum terdaftar ditolak. `host` terisi otomatis dengan nama mesin penulis kalau dikosongkan.

Alias diatur per mesin di `~/.8memory/config.json`, sehingga referensi yang sama berlaku di semua mesin dan AI tidak melihat path aslinya:

```json
{ "aliases": { "backup": "D:\\Backup" } }
```

```json
{ "aliases": { "backup": "/srv/backup" } }
```

Layar detail memori di TUI menampilkan lokasi file di mesin ini. Dari terminal, pakai `8memory resolve backup://namaproyek/2026-11-09/database.sql`.

### Memblokir akses AI ke folder backup

8memory hanya mengontrol apa yang masuk ke memori, bukan apa yang bisa dibaca AI. Pemblokiran dilakukan di client. Di Claude Code, tambahkan ke `~/.claude/settings.json`:

```json
{
  "permissions": {
    "deny": ["Read(//d/Backup/**)", "Edit(//d/Backup/**)"]
  }
}
```

Claude Code mengubah path Windows ke bentuk POSIX sebelum mencocokkan, jadi `D:\Backup` ditulis `//d/Backup`. Bentuk `Read(D:/Backup/**)` dibaca sebagai path relatif dan tidak melindungi folder itu. Di Linux, tulis `Read(//srv/backup/**)`.

Aturan `Read` berlaku untuk tool bawaan yang membaca file dan perintah Bash yang dikenali seperti `cat`, `head`, dan `sed`. Aturan ini tidak berlaku untuk perintah yang membaca file tanpa menyebut namanya, misalnya `grep -r` dari dalam folder itu, atau skrip Python dan Node yang membuka file sendiri. Untuk mempersempit celah itu, tambahkan hook `PreToolUse` yang opsional. Hook ini berjalan di Git Bash, dan menolak setiap panggilan tool yang menyebut folder backup:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Read|Edit|Write|Grep|Glob",
        "hooks": [{
          "type": "command",
          "command": "grep -qiE '(D:[/\\\\]+|/d/)Backup' && { echo 'Folder backup tidak boleh diakses. Gunakan referensi backup://.' >&2; exit 2; } || exit 0"
        }]
      }
    ]
  }
}
```

Hook ini hanya mencocokkan teks, jadi perintah yang menyusun path secara tidak langsung tetap lolos. Untuk pemblokiran di level sistem operasi, aktifkan sandbox Claude Code. `8memory resolve` mengembalikan path asli, jadi tambahkan `Bash(*8memory* resolve*)` ke `deny` kalau AI tidak boleh memakainya.

## Tool MCP

| Tool | Parameter |
| --- | --- |
| `journey_mark` | `title` (wajib), `cwd`, `project`, `branch`, `agent` |
| `journey_list` | `period` (`1d`, `7d`, `30d`, `all`), `cwd`, `project`, `branch`, `limit` |
| `memory_write` | `change`, `feature`, `content` (wajib), `files`, `node_id`, `cwd`, `project`, `branch`, `tags`, `agent` |
| `memory_search` | `query`, `cwd`, `project`, `branch`, `change`, `node_id`, `limit` |

Pembacaan default-nya proyek dan branch saat ini. Nilai `*` pada `project` atau `branch` membaca semuanya.

Sintaks query sama di semua backend: kata-kata harus cocok semua, `"frasa persis"`, `awalan*`, dan `OR` di antara alternatif.

## Test

```sh
go test ./...
```

Backend server ikut diuji kalau env var berikut berisi JSON konfigurasi: `MEMORY8_TEST_POSTGRES`, `MEMORY8_TEST_SQLSERVER`, `MEMORY8_TEST_ORACLE`. Contoh:

```sh
MEMORY8_TEST_POSTGRES='{"backend":"postgres","host":"localhost","port":5432,"user":"postgres","password":"x","database":"postgres","options":"sslmode=disable"}' go test ./...
```

Test menambah baris dengan nama project unik dan tidak menghapus apa pun, jadi gunakan database khusus test.
