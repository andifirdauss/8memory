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
8memory
```

Di PowerShell, dari folder program, ketik `.\8memory`. Supaya bisa dipanggil dari mana saja, tambahkan foldernya ke `PATH`.

- **Journey:** linimasa prompt per 1 hari, 7 hari, 1 bulan, atau semua (`p`). Dikelompokkan menurut proyek/branch, proyek, branch, hari, atau agent (`g`). Enter membuka memori milik node itu, dan `x` menghapus attachment node itu.
- **Memory:** filter periode (`p`), grup (`g`), jenis perubahan (`c`), dan pencarian (`/`). Enter membuka isi lengkap.
- **Rules:** lihat, tambah, ubah, dan hapus aturan yang berlaku global, per proyek, atau per branch.
- **Chat:** bicara dengan AI dari sini, dengan CLI asli provider bekerja di belakang layar. Lihat [Chat](#chat).
- **Storage:** ganti backend dan salin data yang sudah ada. Penyalinan aman diulang. Restart AI client setelah mengganti backend.

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

## Chat

Chat adalah layar percakapan di dalam TUI. Setiap prompt diketik di 8memory, diperkaya dengan aturan dan memori, lalu dijalankan oleh CLI asli provider di belakang layar. Jawabannya tampil di TUI, dan percakapannya berlanjut dari prompt ke prompt.

1. Buka menu **Chat**. Tekan enter di baris Provider untuk memilih dari daftar, lalu isi folder proyek. Proyek dan branch dideteksi dari folder itu.
2. Tulis prompt. Tambahkan attachment kalau perlu.
3. Tekan enter. Prompt langsung terkirim. Layar konfirmasi hanya muncul kalau ada attachment yang tidak bisa dikirim.
4. Setiap prompt otomatis mendapat node journey sendiri dan ringkasan. Kalau ada aturan yang terpotong dari batas 2 KB, TUI memberi tahu.

Ringkasan dibatasi 2 KB dan berisi aturan yang berlaku serta catatan bahwa proyek ini punya memori 8memory. Aturan tidak pernah dipendekkan, urutannya branch, proyek, lalu global.

Isi memori tidak disalin ke ringkasan. AI sendiri yang mencarinya dengan `memory_search` saat perlu dan menulis hasil kerjanya dengan `memory_write`. Chat menyambungkan tool 8memory ke CLI secara otomatis di setiap prompt, jadi `claude mcp add` tidak diperlukan untuk Chat, dan tool itu berjalan tanpa dialog izin. Ringkasan meminta AI memperlakukan hasil pencarian sebagai data, bukan instruksi. Judul node diambil dari teks yang diketik.

| Provider | Dijalankan dengan | Ringkasan | Teks tempelan dan file | Gambar |
| --- | --- | --- | --- | --- |
| Claude Code | `claude -p --resume` | catatan dan aturan di `--append-system-prompt`, ID node di awal prompt | referensi `@path` | path gambar di dalam prompt |
| Codex | `codex exec resume --json` | di awal prompt | tidak didukung, muncul peringatan dan tidak dikirim | `--image` |

Di Claude Code, ID node ditaruh di awal pesan, bukan di system prompt. System prompt yang berubah di setiap prompt akan membatalkan cache seluruh percakapan dan memboroskan kuota.

### Izin tool

Claude Code berjalan dalam auto mode. Tindakan yang masih butuh izin muncul sebagai dialog di TUI, dengan seluruh isi tool call. **Ctrl+Y** mengizinkan dan **Esc** menolak. Mengizinkan sengaja tidak memakai `y`, karena dialog bisa terbuka saat kamu sedang mengetik prompt berikutnya. Dialog ini dilayani server MCP yang hanya mendengarkan `127.0.0.1` selama sesi, dengan token acak.

`permissions.deny`, `ask`, dan hook di setting Claude Code tetap berlaku.

Codex tidak punya dialog izin dalam mode ini. Codex berjalan di sandbox `workspace-write`, jadi hanya bisa menulis di folder proyek.

CLI dicari di `PATH`, lalu di `~/.local/bin` untuk Claude Code. Provider baru ditambahkan dengan mengimplementasikan interface `Provider` di `internal/tocli/provider.go`.

### Perintah

Ketik `/` di awal prompt untuk membuka menu. ↑/↓ memilih, Tab melengkapi, Enter menjalankan, dan Esc menutup menu.

| Perintah | Fungsi |
| --- | --- |
| `/help` | Daftar perintah dan tombol |
| `/new`, `/clear` | Membersihkan layar dan memulai percakapan baru di folder yang sama. Percakapan lama tetap tersimpan di CLI provider |
| `/usage` | Sisa kuota provider, tanpa memakai kuota. Codex tidak menyediakannya di mode ini |
| `/exit` | Mengakhiri sesi |

Teks lain yang diawali `/` diteruskan ke CLI provider sebagai prompt biasa, termasuk node journey dan ringkasannya.

### Tombol editor

| Tombol | Fungsi |
| --- | --- |
| Enter | Kirim prompt |
| Shift+Enter, Ctrl+J | Baris baru |
| Alt+Enter | Baris baru, tetapi Windows Terminal memakainya untuk fullscreen secara default |
| Alt+V | Tempel gambar dari clipboard |
| Ctrl+O | Tambah file dari path |
| Ctrl+E | Daftar attachment: enter untuk pratinjau, `d` untuk hapus |
| Backspace, Delete | Menghapus penanda attachment sebagai satu kesatuan |
| PgUp, PgDn | Menggulir percakapan |
| Esc, Ctrl+C | Menghentikan prompt yang sedang berjalan |
| Esc dua kali | Mengakhiri sesi saat tidak ada prompt berjalan |

Ctrl+V di Windows Terminal hanya menempel teks, karena terminal tidak mengirim gambar lewat paste. Karena itu gambar memakai Alt+V, yang membaca clipboard lewat PowerShell di Windows, `osascript` di macOS, serta `wl-paste` atau `xclip` di Linux.

### Attachment

Teks tempelan sepanjang 20 baris atau 2 KB ke atas otomatis menjadi attachment, dan di editor hanya tampil penanda seperti `[Paste #1 · 120 baris]`. Ambangnya diatur di `~/.8memory/config.json`:

```json
{ "paste_lines": 40, "paste_bytes": 4096 }
```

Attachment disimpan di `~/.8memory/attachments/<uuid node>/`, bukan di folder proyek. Isinya tidak dihitung dalam batas 2 KB dan tidak masuk ke memori. Untuk menghapusnya, tekan `x` pada node di layar Journey, atau hapus folder itu.

### Batasan

- Chat hanya memperkuat kepatuhan AI, tidak menjaminnya. Aturan tetap berupa teks yang bisa diabaikan model.
- Larangan yang wajib tetap perlu `permissions.deny` atau hook. Lihat [Memblokir akses AI ke folder backup](#memblokir-akses-ai-ke-folder-backup).
- Hanya prompt yang diketik di layar Chat yang mendapat ringkasan. Prompt yang diketik langsung di CLI provider tidak lewat Chat.
- Tampilan jawaban buatan 8memory, bukan UI asli CLI. Perintah khusus terminal seperti `/login` tidak tersedia, dan login tetap dilakukan di CLI provider.
- Sesi non-interaktif tidak menampilkan dialog kepercayaan folder, jadi hook dan server MCP milik proyek ikut berjalan. Pilih hanya folder yang kamu percaya.
- `claude -p` memakai batas langganan yang sama dengan Claude Code biasa selama kamu login dengan paket Claude, sesuai [ketentuan Anthropic](https://support.claude.com/en/articles/15036540).
- Kalau hook `UserPromptSubmit` aktif, node dari Chat dipakai dan prompt tidak tercatat dua kali.
- Isi percakapan hanya ada di layar selama sesi. Yang tersimpan di 8memory hanya node journey dan memori yang ditulis AI.

Perilaku editor diadaptasi dari [oh my pi](https://github.com/can1357/oh-my-pi). Kodenya ditulis sendiri untuk proyek ini.

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

Test `MEMORY8_TEST_CLIPBOARD=1` membaca clipboard sungguhan dan dilewati secara default.

Backend server ikut diuji kalau env var berikut berisi JSON konfigurasi: `MEMORY8_TEST_POSTGRES`, `MEMORY8_TEST_SQLSERVER`, `MEMORY8_TEST_ORACLE`. Contoh:

```sh
MEMORY8_TEST_POSTGRES='{"backend":"postgres","host":"localhost","port":5432,"user":"postgres","password":"x","database":"postgres","options":"sslmode=disable"}' go test ./...
```

Test menambah baris dengan nama project unik dan tidak menghapus apa pun, jadi gunakan database khusus test.
