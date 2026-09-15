# mdcloud — markdown cloud for [mathmd](https://github.com/denizsincar29/mathmd)

A small server that keeps markdown documents at `mdcloud.example/<username>/<subfolder>/<file>`,
shows them as clean read-only pages, collects comments (anonymous or logged in),
and opens the mathmd editor on a document when you want to change it.

```mermaid
sequenceDiagram
    participant B as Browser
    participant C as mdcloud
    participant E as mathmd
    B->>C: POST /api/auth/login → Set-Cookie: mdcloud_sid (Domain=.example)
    B->>C: GET /deniz/ДЗ/ИИ (preview)
    B->>E: opens mathmd/#cloud=deniz/ДЗ/ИИ
    E->>C: GET /api/docs/deniz/ДЗ/ИИ (cookie rides along)
    E->>C: PUT /api/docs/deniz/ДЗ/ИИ (Ctrl+S in the editor)
```

Both sites live under one registrable domain, so the session is a single
**httpOnly cookie** (`Secure`, `SameSite=Lax`) that the browser sends to the
cloud and to the editor alike: log in once, edit anywhere. The path of the
document to open travels in the URL *fragment* (`#cloud=…`), which browsers
never send to a server.

- **Stack:** Go (stdlib `net/http`), GORM, PostgreSQL. One binary, no CGO.
- **Documents** are rows in the database (`owner_id` + `path`), so moving,
  backing up or re-permissioning is an `UPDATE`, not a file shuffle.
- **Visibility** is per document: `public` or `private`, private is the default.
- **Comments** can be anonymous (with a name you type) or from a logged-in user.
- **Registration is by invitation.** The first account is the cloud owner: they
  pass without a code and hand out one-time codes to everyone else.
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

1. asks for the domain, the mathmd address, the shared cookie domain and the
   database role/name, and writes `.env` (mode 600, generated password and IP
   salt) — later runs just read it;
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
MDCLOUD_COOKIE_DOMAIN=example \
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
| `MDCLOUD_EDITOR_URL` | `https://mathmd.denizsincar.ru` | where «Редактировать» sends you |
| `MDCLOUD_COOKIE_DOMAIN` | empty | cookie domain; set it to the parent domain to share the login with the editor |
| `MDCLOUD_COOKIE_NAME` | `mdcloud_sid` | session cookie name |
| `MDCLOUD_ALLOWED_ORIGINS` | empty | extra CORS origins (cloud and editor origins are always allowed) |
| `MDCLOUD_ALLOW_REGISTRATION` | `false` | `true` opens registration to everyone without an invite |
| `MDCLOUD_SESSION_TTL` | `720h` | session lifetime |
| `MDCLOUD_INVITE_TTL` | `336h` | default invite lifetime (14 days) |
| `MDCLOUD_COMMENT_LIMIT` / `MDCLOUD_COMMENT_WINDOW` | `10` / `10m` | per-IP comment rate limit |
| `MDCLOUD_MAX_DOC_BYTES` | `2097152` | document size cap |
| `MDCLOUD_STATIC_DIR` | unset | serve this directory at `/` (development only) |

The `Secure` flag on the cookie follows `MDCLOUD_BASE_URL`: https → `Secure`,
so a plain-http development setup still works.

## API

Browser sessions ride in a cookie; scripts may use `Authorization: Bearer <token>`
(the token from `login`/`register`) instead — both reach the same session table.
State-changing requests authenticated by cookie must carry an allowed `Origin`
and a JSON body (see *Security notes*).

| Method | Path | Who | What |
| --- | --- | --- | --- |
| `POST` | `/api/auth/register` | invited | `{username, password, email?, display_name?, invite?}` → token + cookie |
| `POST` | `/api/auth/login` | anyone | `{login, password}` (username or email) → token + cookie |
| `POST` | `/api/auth/logout` | signed in | drop the session, clear the cookie |
| `GET` | `/api/me` | signed in | current user and document count |
| `GET` | `/api/config` | anyone | registration mode (`first`/`open`/`invite`/`closed`), cloud and editor URLs |
| `GET` | `/api/docs` | signed in | all of your documents, private included |
| `GET` | `/api/docs/{owner}` | anyone | that user's public documents |
| `GET` | `/api/docs/{owner}/{path...}` | anyone | one document with its markdown |
| `PUT` | `/api/docs/{owner}/{path...}` | owner | create/update `{title?, content?, visibility?, comments_on?, comments_require_auth?}` |
| `DELETE` | `/api/docs/{owner}/{path...}` | owner | soft delete |
| `GET` | `/api/comments/{owner}/{path...}` | anyone | comments (private docs excluded) |
| `POST` | `/api/comments/{owner}/{path...}` | anyone | `{body, name?}` — name is required when anonymous |
| `DELETE` | `/api/comments/{id}` | author or doc owner | delete a comment |
| `GET` | `/api/invites` | admin | issued invites and their state (codes are never returned) |
| `POST` | `/api/invites` | admin | `{note?, days?}` → `{code, url}` — shown once |
| `DELETE` | `/api/invites/{id}` | admin | revoke an unspent invite |
| `GET` | `/api/health` | anyone | liveness |

Paths are normalised: no leading slash, no `.`/`..` segments, at most 5 nested
folders, letters/digits/`.`/`-`/`_`/`+`/`()` in a segment.

## Invites

The first registered account is the owner (`is_admin`) and needs no code. Every
account after that needs an invite — a 16-byte code, stored only as a SHA-256
hash, so the link is shown exactly once, when it is created, and a database dump
hands nobody a way in. A code is spent in the same transaction that creates the
account, so two people cannot use one code and a failed signup does not burn it.

`POST /api/invites` returns `https://<cloud>/#invite=<code>`; the code lives in
the URL fragment and never reaches the server or its logs.

If an existing cloud ends up with no admin at all (accounts created before
invites existed), the oldest account is promoted to owner at startup — there has
to be somebody who can hand out codes.

## Security notes

- Session tokens and invite codes are stored as SHA-256 hashes.
- The session cookie is `HttpOnly` (unreachable from JavaScript) and
  `SameSite=Lax`. A shared parent-domain cookie is visible to every subdomain
  you run — that is the price of one login for both sites, and it is worth it
  only when the whole parent domain is yours.
- CSRF: the browser attaches the cookie on its own, so state-changing requests
  that arrive with a cookie must carry an allowed `Origin`; requests without a
  cookie (scripts, `Bearer`) are not affected. HTML forms cannot send
  `application/json` cross-origin without a preflight, and our CORS reflects
  only allowlisted origins — so form-based CSRF never reaches a handler.
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
internal/models         users, docs, comments, sessions, invites
internal/store          Postgres connection, AutoMigrate
internal/auth           bcrypt, tokens, invite codes
internal/mdpath         document path rules
internal/api            routes and handlers
web/                    preview, login, registration, invites (static, served by Caddy)
deploy.sh               build + database + systemd + Caddy, idempotent
deploy/Caddyfile.snippet
```

## Tests

```bash
go test ./...
```

Tests run against an in-memory SQLite database, so they need neither Postgres
nor the network. They cover document visibility, ownership, comment rules,
first-user/admin rules, invite redemption and expiry, cookie flags, CSRF and
CORS, token lifecycle and path validation.

The web page has its own smoke test (jsdom, no browser):

```bash
npm install -g jsdom
node web/test/ui.test.cjs
```

It drives registration, the invite link, issuing and revoking invites and the
error states against a stubbed API.

## Not done yet

- The preview renders markdown in the browser; it does not yet reproduce the
  mathmd viewer (MathJax formulas, Desmos graphs, frontmatter-driven module
  loading). The editor opens the document today — the preview will start
  reusing the editor's render pipeline when mathmd grows a read-only viewer mode.
- No password reset by email, no admin UI for users (only for invites).
- Comments are not paginated.
- One process, one rate limiter in memory; a second node would need a shared
  counter.

## License

MIT
