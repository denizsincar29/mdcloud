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
- **Creating a document** starts in the editor: your index page has a path
  field and a **Создать документ** button that opens mathmd on that address;
  `Ctrl+S` there creates the document in the cloud.
- **The document list is a tree:** the folder is a heading (`/` for the cloud
  root), the files sit under it by name alone, so a screen reader can jump
  between folders by heading instead of listening to the same path prefix over
  and over. The open document shows its full address and has a
  **Переименовать** button next to it, which moves the document (content and
  comments travel along, an occupied address answers `409`).
- **Visibility** is per document: `public` or `private`, private is the default.
- **Comments** can be anonymous (with a name you type) or from a logged-in user.
  A comment can point at a line of the document: `{line 5}` renders as a link
  labelled «строка 5», and `[здесь]{line 5}` uses the bracketed text as its
  label, like a markdown hyperlink. The editor button **Указать на строку
  документа** walks the document blocks with the arrow keys and inserts the
  reference at the caret — with the selection wrapped, if there is one. `Alt+B`
  jumps back to the comment anchor you left from (a button or such a link).
- **Registration is by invitation.** The first account is the cloud owner: they
  pass without a code and hand out one-time codes to everyone else.
- **One markdown — one look.** The preview renders a document exactly like the
  mathmd editor does: showdown, MathJax 4 (LaTeX and AsciiMath, with hidden
  MathML for screen readers), chess boards, Desmos graphs, frontmatter
  stripped. The page runs under a CSP that keeps scripts to itself and
  jsdelivr and has no `unsafe-inline` for scripts, so nothing written in a
  document can execute. A graph is the exception and gets its own document: see
  *Graphs live in their own document* below.

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
instead — either the token from `login`/`register` (a session) or a permanent API
key issued from the account menu (`POST /api/tokens`). A key is a hash in
`api_tokens`, carries an optional expiry and can be revoked; it grants the same
access as the account's password, so it is shown exactly once.

State-changing requests authenticated by cookie must carry an allowed `Origin`
and a JSON body (see *Security notes*). Bearer requests are exempt: no cookie,
and a foreign page cannot set the header.

An assistant-readable walkthrough of the whole surface lives at
`GET /api/llm.md` (public, `text/markdown`, embedded in the binary from
`internal/api/llm.md`).

| Method | Path | Who | What |
| --- | --- | --- | --- |
| `POST` | `/api/auth/register` | invited | `{username, password, email?, display_name?, invite?}` → token + cookie |
| `POST` | `/api/auth/login` | anyone | `{login, password}` (username or email) → token + cookie |
| `POST` | `/api/auth/logout` | signed in | drop the session, clear the cookie |
| `GET` | `/api/me` | signed in | current user and document count |
| `GET` | `/api/config` | anyone | registration mode (`first`/`open`/`invite`/`closed`), cloud and editor URLs |
| `GET` | `/api/docs` | signed in | all of your documents, private included |
| `POST` | `/api/docs` | signed in | save `{path, content?, title?, public?, visibility?, expires_in_days?, …}` under the caller → `201` created / `200` updated |
| `GET` | `/api/docs/{owner}` | anyone | that user's public documents |
| `GET` | `/api/docs/{owner}/{path...}` | anyone | one document with its markdown |
| `PUT` | `/api/docs/{owner}/{path...}` | owner | create/update `{title?, content?, visibility?, comments_on?, comments_require_auth?, expires_in_days?}` |
| `PATCH` | `/api/docs/{owner}/{path...}` | owner | `{path}` — перенести на другой адрес (`409`, если адрес занят) |
| `DELETE` | `/api/docs/{owner}/{path...}` | owner | soft delete |
| `GET` | `/api/comments/{owner}/{path...}` | anyone | comments (private docs excluded) |
| `POST` | `/api/comments/{owner}/{path...}` | anyone | `{body, name?}` — name is required when anonymous |
| `DELETE` | `/api/comments/{id}` | author or doc owner | delete a comment |
| `GET` | `/api/invites` | admin | issued invites and their state (codes are never returned) |
| `POST` | `/api/invites` | admin | `{note?, days?}` → `{code, url}` — shown once |
| `DELETE` | `/api/invites/{id}` | admin | revoke an unspent invite |
| `GET` | `/api/tokens` | signed in | your API keys (labels, terms, last use — never the keys) |
| `POST` | `/api/tokens` | signed in | `{label?, days?}` → `{id, token, expires_at}` — value shown once; no `days` means forever |
| `DELETE` | `/api/tokens/{id}` | signed in | revoke your key |
| `GET` | `/api/llm.md` | anyone | how to use this API, written for assistants |
| `GET` | `/api/health` | anyone | liveness |

Paths are normalised: no leading slash, no `.`/`..` segments, at most 5 nested
folders, letters/digits/`.`/`-`/`_`/`+`/`()` in a segment. Cyrillic is fine —
paths are what the owner typed, and they are the key (`idx_doc_owner_path`).

### Two forms of an address: `path` and `slug`

A document has a Russian `path` (`ДЗ/ИИ/задачи`) and a derived Latin `slug`
(`dz/ii/zadachi`, `internal/mdpath`). The slug is what the link carries: Cyrillic
in a URL becomes `%D0%94…`, which cannot be dictated or read off the screen.
Every lookup (`GET`/`PUT`/`PATCH`/`DELETE`) accepts either form and resolves to
the same row, so links handed out before the slug existed keep working. The slug
is recomputed from the path on every write and backfilled for old rows at
startup, so the two cannot drift; two paths with one slug (`ДЗ` and `dz`) are
refused with `409` — a link must point at exactly one document.

### Documents with a term

`expires_in_days` (`0` clears it, max 3650) turns a document into a temporary
one — homework for a teacher, a draft to share and forget. While the term runs
it opens like any other; once `expires_at` passes the document answers `404`
even for its owner and drops out of the lists, and a sweeper running every 15
minutes hard-deletes the row and its comments. Deletion is final on purpose:
the point of a term is that the content is gone, not merely hidden.

## Invites

The first registered account is the owner (`is_admin`) and needs no code. Every
account after that needs an invite — a 16-byte code, stored only as a SHA-256
hash, so the link is shown exactly once, when it is created, and a database dump
hands nobody a way in. A code is spent in the same transaction that creates the
account, so two people cannot use one code and a failed signup does not burn it.

`POST /api/invites` returns `https://<cloud>/#invite=<code>`; the code lives in
the URL fragment and never reaches the server or its logs. The invited person
gets the link and nothing else — the registration form has no code field, the
code rides in the fragment and is sent along with the signup.

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
- A document renders as the editor renders it — no sanitizer sits between
  showdown and the page. What keeps that safe is the CSP on the page, not an
  allowlist of tags: `script-src` allows only the origin and jsdelivr and has
  neither `unsafe-inline` nor `unsafe-eval`, so an inline `<script>`, an
  `onclick=` attribute or a `javascript:` link in a document does not run;
  `form-action 'self'` stops a form from posting credentials elsewhere; frames
  are limited to the site itself — the only frame is the graph embed. Strip the
  CSP (a static host that does not let you set headers, say) and the page is no
  longer safe — put DOMPurify back before that.
- `style-src` does carry `'unsafe-inline'`: MathJax and chessjax build their
  stylesheets inside the page. Injected CSS is an annoyance, not an execution
  path.
- Third-party scripts are pinned to jsdelivr in the CSP. Self-hosting showdown,
  MathJax and chessjax under `web/vendor/` and dropping the CDN from the policy
  is a small change if you would rather not depend on it.

### Graphs live in their own document

A Desmos graph is not drawn on the document page: the page puts an
`<iframe src="/embed/desmos#…">` where the ` ```desmos ` block was, and the
graph itself is built by `web/embed-desmos.*` — a document with nothing in it
but the calculator. Caddy gives that one path its own policy, in
`deploy/Caddyfile.snippet`.

The reason is `'unsafe-eval'`: the Desmos SDK evaluates strings as code and
will not start without it (`Desmos.Calculator is not a function` — the failing
probe is in the commit message). Allowing that on the document page would
remove exactly the property the page relies on, since a document is somebody
else's markdown and no sanitizer stands between showdown and the page. In the
embed document there is nothing to protect: the expressions arrive in the URL
fragment, go to the calculator as LaTeX strings, and are never assembled into
markup. The parent page keeps `frame-src 'self'` and no `unsafe-eval`.

The API key in `embed-desmos.html` is the public demo key, the same the editor
uses. Nothing is loaded for documents without a ` ```desmos ` block.

## Layout

```
main.go                 wiring, startup, graceful shutdown
internal/config         environment
internal/models         users, docs, comments, sessions, invites, API keys
internal/store          Postgres connection, AutoMigrate
internal/auth           bcrypt, tokens, invite codes
internal/mdpath         document path rules
internal/api            routes and handlers
internal/api/llm.md     API guide for assistants, served at /api/llm.md
web/                    preview, login, registration, invites, API keys (static, served by Caddy)
web/md.mjs              markdown → HTML for the preview (frontmatter, AsciiMath)
web/embed-desmos.*      one Desmos graph per document, framed by the preview
web/test/               page smoke test (jsdom) and markdown test (node)
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

The web page has its own smoke tests (no browser):

```bash
npm install -g jsdom showdown
node web/test/ui.test.cjs   # screens, buttons, API calls (jsdom + stubbed fetch)
node web/test/md.test.mjs   # markdown → HTML: frontmatter, AsciiMath, formulas
```

`ui.test.cjs` drives registration, the invite link, issuing and revoking
invites, the create-document button, the folder tree, renaming, the desmos
frame and the error states against a stubbed API.
`md.test.mjs` checks what happens to a document on the way to the page —
frontmatter goes, backticks survive as AsciiMath delimiters instead of turning
into `<code>`, LaTeX reaches the page untouched — first against a stub, then
against the real showdown if it is installed.

## Not done yet

- A Desmos graph is a live calculator, so a document with a graph is neither
  printable nor available offline. Documents without graphs load nothing
  extra.
- The graph embed always starts a calculator, even when the reader only
  wanted the text; a static picture of the graph would be lighter, but then
  the graph would stop being readable by a screen reader.
- No password reset by email, no admin UI for users (only for invites).
- Comments are not paginated.
- One process, one rate limiter in memory; a second node would need a shared
  counter.

## License

MIT
