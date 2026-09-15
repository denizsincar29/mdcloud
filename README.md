# mdcloud — markdown cloud for [mathmd](https://github.com/denizsincar29/mathmd)

A small server that keeps markdown documents at `mdcloud.example/<username>/<subfolder>/<file>`,
shows them as clean read-only pages, collects comments (anonymous or logged in),
and hands you off to the mathmd editor when you want to change something.

```mermaid
sequenceDiagram
    participant B as Browser
    participant C as mdcloud
    participant E as mathmd
    B->>C: GET /deniz/ДЗ/ИИ (preview)
    B->>C: POST /api/handoff (Bearer token)
    C-->>B: {code, url: mathmd/#cloud=CODE}
    B->>E: opens editor, reads the code from the fragment
    E->>C: POST /api/handoff/redeem {code}
    C-->>E: {token, owner, path} — code is burned
    E->>C: PUT /api/docs/deniz/ДЗ/ИИ (Bearer token)
```

The two sites never share a cookie. mdcloud issues a **single-use code**, puts it
in the URL *fragment* (which browsers never send to a server), and mathmd trades
that code for a normal session token. Codes live 90 seconds by default.

- **Stack:** Go (stdlib `net/http`), GORM, PostgreSQL. One binary, no CGO.
- **Documents** are rows in the database (`owner_id` + `path`), so moving,
  backing up or re-permissioning is an `UPDATE`, not a file shuffle.
- **Visibility** is per document: `public` or `private`, private is the default.
- **Comments** can be anonymous (with a name you type) or from a logged-in user.
- **Markdown is never executed as HTML.** The API returns markdown text; the
  preview renders it through showdown + DOMPurify with an allowlist, and the
  page runs under a CSP that has no `unsafe-inline` and no `unsafe-eval`.

## Install

On a Debian/Ubuntu box with PostgreSQL and Caddy:

```bash
git clone https://github.com/denizsincar29/mdcloud.git ~/mdcloud
cd ~/mdcloud
./deploy.sh
```

Run it as your normal user, not as root: the script calls `sudo` itself for the
steps that need it (postgres, systemd, `/etc/caddy`), and the service runs as
whoever started the script. `deploy.sh` is idempotent — run it again for every
redeploy. On the first run it

1. asks for the domain, the mathmd address and the database role/name, and
   writes `.env` (mode 600, generated password and IP salt) — later runs just
   read it;
2. builds the binary (installing Go into `~/go-root` if the server has none);
3. creates the PostgreSQL role and database through `sudo -u postgres psql`;
4. installs and starts the `mdcloud` systemd unit;
5. picks a free port if the configured one is taken, syncs `web/` to
   `/var/www/html/mdcloud` and appends the site block to
   `/etc/caddy/Caddyfile` — with `caddy validate` first and a rollback if the
   config is rejected.

Settings can be overridden per run: `MDCLOUD_ADDR=127.0.0.1:8092 ./deploy.sh`.
Database settings (`MDCLOUD_DATABASE_URL`, role, password, IP salt) stay
`.env`-only on purpose — the deployer and systemd must read the same values.

Useful flags: `--reconfigure` (re-ask the settings), `--no-caddy` (leave the web
server alone).

### Manual setup

```bash
go build -o mdcloud .
MDCLOUD_DATABASE_URL='postgres://user:pass@localhost:5432/mdcloud?sslmode=disable' \
MDCLOUD_IP_SALT="$(openssl rand -hex 24)" \
MDCLOUD_BASE_URL=https://mdcloud.example \
MDCLOUD_EDITOR_URL=https://mathmd.example \
MDCLOUD_ALLOWED_ORIGINS=https://mdcloud.example,https://mathmd.example \
./mdcloud
```

The schema migrates itself at startup. Put Caddy (or nginx) in front: static
`web/` at the root, `/api/*` proxied to `127.0.0.1:8080`. Ready-made Caddy block
is in `deploy/Caddyfile.snippet`.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `MDCLOUD_ADDR` | `127.0.0.1:8080` | listen address |
| `MDCLOUD_DATABASE_URL` | — (required) | PostgreSQL DSN (`DATABASE_URL` also works) |
| `MDCLOUD_IP_SALT` | — (required) | salt for hashing commenter IPs |
| `MDCLOUD_BASE_URL` | `http://` + addr | public URL of the cloud |
| `MDCLOUD_EDITOR_URL` | `https://mathmd.denizsincar.ru` | where the edit button sends you |
| `MDCLOUD_ALLOWED_ORIGINS` | empty | CORS allowlist, comma separated |
| `MDCLOUD_ALLOW_REGISTRATION` | `true` | `false` closes signup after the first user |
| `MDCLOUD_SESSION_TTL` | `720h` | session token lifetime |
| `MDCLOUD_HANDOFF_TTL` | `90s` | handoff code lifetime |
| `MDCLOUD_COMMENT_LIMIT` / `MDCLOUD_COMMENT_WINDOW` | `10` / `10m` | per-IP comment rate limit |
| `MDCLOUD_MAX_DOC_BYTES` | `2097152` | document size cap |
| `MDCLOUD_STATIC_DIR` | unset | serve this directory at `/` (development only) |

## API

Auth is `Authorization: Bearer <token>`; there are no cookies anywhere.

| Method | Path | Who | What |
| --- | --- | --- | --- |
| `POST` | `/api/auth/register` | anyone | `{username, password, email?, display_name?}` → token |
| `POST` | `/api/auth/login` | anyone | `{login, password}` (username or email) → token |
| `POST` | `/api/auth/logout` | signed in | drop the current token |
| `GET` | `/api/me` | signed in | current user and document count |
| `GET` | `/api/docs` | signed in | all of your documents, private included |
| `GET` | `/api/docs/{owner}` | anyone | that user's public documents |
| `GET` | `/api/docs/{owner}/{path...}` | anyone | one document with its markdown |
| `PUT` | `/api/docs/{owner}/{path...}` | owner | create/update `{title?, content?, visibility?, comments_on?, comments_require_auth?}` |
| `DELETE` | `/api/docs/{owner}/{path...}` | owner | soft delete |
| `GET` | `/api/comments/{owner}/{path...}` | anyone | comments (private docs excluded) |
| `POST` | `/api/comments/{owner}/{path...}` | anyone | `{body, name?}` — name is required when anonymous |
| `DELETE` | `/api/comments/{id}` | author or doc owner | delete a comment |
| `POST` | `/api/handoff` | signed in | `{path}` → `{code, url, expires_at}` |
| `POST` | `/api/handoff/redeem` | anyone with a code | `{code}` → `{token, owner, path}` |
| `GET` | `/api/health` | anyone | liveness |

Paths are normalised: no leading slash, no `.`/`..` segments, at most 5 nested
folders, letters/digits/`.`/`-`/`_`/`+`/`()` in a segment.

## Security notes

- Session tokens and handoff codes are stored as SHA-256 hashes; a database dump
  does not hand anybody a login.
- Handoff codes are burned by a transactional read-and-delete, so two parallel
  requests cannot both spend one code.
- Commenter IPs are stored only as `sha256(salt + ip)` and are used solely for
  rate limiting.
- Markdown from the database is sanitised on the client with an allowlist.
  `<script>`, `<iframe>`, event handlers and `javascript:` URLs never reach the
  DOM; chess boards survive because `chessjax-board` is the one custom element
  allowed through.
- The API is served under a CSP with no `unsafe-inline`, so even a sanitizer
  bypass has nothing to execute.
- Third-party scripts are pinned to jsdelivr in the CSP. Self-hosting showdown
  and DOMPurify under `web/vendor/` and dropping the CDN from the policy is a
  two-line change if you would rather not depend on it.

## Layout

```
main.go                 wiring, startup, graceful shutdown
internal/config         environment
internal/models         users, docs, comments, sessions
internal/store          Postgres connection, AutoMigrate
internal/auth           bcrypt, tokens, one-time codes
internal/mdpath         document path rules
internal/api            routes and handlers
web/                    read-only preview (static, served by Caddy)
deploy.sh               build + database + systemd + Caddy, idempotent
deploy/Caddyfile.snippet
```

## Tests

```bash
go test ./...
```

Tests run against an in-memory SQLite database, so they need neither Postgres
nor the network. They cover document visibility, ownership, comment rules,
single-use handoff codes, token lifecycle, CORS and path validation.

## Not done yet

- The preview renders markdown in the browser; it does not yet reproduce the
  mathmd viewer (MathJax formulas, Desmos graphs, frontmatter-driven module
  loading). The handoff to the editor works today — the preview will start
  reusing the editor's render pipeline when mathmd grows a read-only viewer mode.
- The editor cannot yet list or save cloud documents by itself: opening a doc
  from mathmd and Ctrl+S straight into the cloud is the next step. The pieces
  are already there — `POST /api/handoff` hands mathmd a token in the URL
  fragment, `GET /api/docs` lists your documents, `PUT /api/docs/{owner}/{path}`
  writes them back.
- No password reset by email, no admin UI.
- One process, one rate limiter in memory; a second node would need a shared
  counter.

## License

MIT
