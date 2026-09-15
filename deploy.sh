#!/usr/bin/env bash
# deploy.sh — разворачивает mdcloud на этом сервере: сборка, база, systemd.
#
# Запускается от обычного пользователя, без sudo: скрипт сам зовёт sudo там,
# где нужно (postgres, systemd, /etc/caddy). Пользователь сервиса — тот, кто
# запустил скрипт; если запустить через sudo, им станет SUDO_USER.
#
# Что делает одним запуском:
#   1. спрашивает настройки (домен, адрес редактора, база) и пишет .env,
#      если его ещё нет — повторный запуск ничего не переспрашивает;
#   2. собирает бинарь mdcloud из исходников;
#   3. создаёт роль и базу в Postgres, если их нет (пароль роли всегда
#      приводится к тому, что в .env);
#   4. создаёт systemd-сервис mdcloud и включает автозапуск;
#   5. раскладывает web/ в /var/www/html/mdcloud и, если попросить, добавляет
#      сайт в Caddyfile (с проверкой конфига и откатом при ошибке).
#
# Идемпотентен: повторный запуск — это обычный редеплой.
#
# Использование:
#   ./deploy.sh                 — развернуть или обновить
#   ./deploy.sh --reconfigure   — переспросить настройки заново
#   ./deploy.sh --no-caddy      — не трогать Caddy
#
# Переменные окружения (все опциональны, имеют приоритет над .env):
#   SERVICE_USER, MDCLOUD_DOMAIN, EDITOR_URL, MDCLOUD_ADDR,
#   DB_USER, DB_PASSWORD, DB_NAME, DB_HOST, DB_PORT, GO

set -euo pipefail

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
say()  { echo -e "${GREEN}==>${NC} $*"; }
warn() { echo -e "${YELLOW}!!!${NC} $*"; }
die()  { echo -e "${RED}ERROR:${NC} $*" >&2; exit 1; }

RECONFIGURE=0
WITH_CADDY=1
for arg in "$@"; do
  case "$arg" in
    --reconfigure) RECONFIGURE=1 ;;
    --no-caddy)    WITH_CADDY=0 ;;
    -h|--help)     sed -n '2,28p' "$0"; exit 0 ;;
    *) die "непонятный аргумент: $arg" ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null || echo "$SCRIPT_DIR")"
cd "$APP_DIR"

SERVICE_NAME="mdcloud"
BIN_NAME="mdcloud"
STATIC_DEST="/var/www/html/mdcloud"
UNIT="/etc/systemd/system/${SERVICE_NAME}.service"
CADDYFILE="/etc/caddy/Caddyfile"

# Сервис работает от того, кто разворачивает: и репозиторий, и .env, и
# запущенный процесс принадлежат ему. Под root'а не лезем.
SERVICE_USER="${SERVICE_USER:-${SUDO_USER:-$(id -un)}}"
if [[ "$SERVICE_USER" == "root" ]]; then
  die "сервис под root не заводим — запусти скрипт от своего пользователя"
fi
if [[ "$(id -un)" == "root" && -z "${SUDO_USER:-}" ]]; then
  die "запусти от обычного пользователя (без sudo) — sudo скрипт позовёт сам"
fi

# Проверяем sudo заранее и предупреждаем о пароле, а не падаем на середине.
# sudo -v тут не годится: при выборочном NOPASSWD в sudoers он всё равно
# требует пароль, хотя нужные нам команды проходят без него.
say "проверяю sudo"
if ! sudo -n true 2>/dev/null; then
  warn "sudo спросит пароль — запускай из терминала, иначе привилегированные шаги упадут"
fi

# ── 1. Настройки ────────────────────────────────────────────────────────────
ENV_FILE="$APP_DIR/.env"

ask() { # ask <переменная> <вопрос> <значение-по-умолчанию>
  local var="$1" question="$2" def="${3:-}" answer=""
  if [[ -n "${!var:-}" ]]; then return 0; fi   # уже задано снаружи — не спрашиваем
  if [[ ! -t 0 ]]; then printf -v "$var" '%s' "$def"; return 0; fi
  read -r -p "$(echo -e "${GREEN}?${NC} $question${def:+ [$def]}: ")" answer || true
  printf -v "$var" '%s' "${answer:-$def}"
}

if [[ ! -f "$ENV_FILE" || "$RECONFIGURE" == "1" ]]; then
  say "настройки (Enter — согласиться с предложенным)"
  ask MDCLOUD_DOMAIN "домен облака (без https://)" "mdcloud.denizsincar.ru"
  ask EDITOR_URL     "адрес редактора mathmd"       "https://mathmd.denizsincar.ru"
  ask MDCLOUD_ADDR   "слушать на"                   "127.0.0.1:8080"
  ask DB_USER        "роль postgres"                "mdcloud"
  ask DB_NAME        "имя базы"                     "mdcloud"
  ask DB_PASSWORD    "пароль роли (можно оставить пустым — сгенерирую)" ""
  ask ALLOW_REG      "открыть регистрацию? (y/n)"   "n"

  DB_HOST="${DB_HOST:-localhost}"
  DB_PORT="${DB_PORT:-5432}"
  MDCLOUD_BASE_URL="https://${MDCLOUD_DOMAIN#*://}"
  MDCLOUD_EDITOR_URL="https://${EDITOR_URL#*://}"
  if [[ -z "${DB_PASSWORD:-}" ]]; then
    DB_PASSWORD="$(openssl rand -hex 16)"   # hex: пароль не сломает ни DSN, ни psql
  fi
  IP_SALT="$(openssl rand -hex 24)"
  case "${ALLOW_REG,,}" in y|yes|да|д) ALLOW_REGISTRATION=true ;; *) ALLOW_REGISTRATION=false ;; esac

  cat > "$ENV_FILE" <<EOF
# Настройки mdcloud. Файл читает и systemd (EnvironmentFile), и deploy.sh.
MDCLOUD_ADDR=$MDCLOUD_ADDR
MDCLOUD_DATABASE_URL=postgres://$DB_USER:$DB_PASSWORD@$DB_HOST:$DB_PORT/$DB_NAME?sslmode=disable
MDCLOUD_BASE_URL=$MDCLOUD_BASE_URL
MDCLOUD_EDITOR_URL=$MDCLOUD_EDITOR_URL
MDCLOUD_ALLOWED_ORIGINS=$MDCLOUD_BASE_URL,$MDCLOUD_EDITOR_URL
MDCLOUD_ALLOW_REGISTRATION=$ALLOW_REGISTRATION
MDCLOUD_IP_SALT=$IP_SALT
MDCLOUD_SESSION_TTL=720h
MDCLOUD_HANDOFF_TTL=90s
MDCLOUD_COMMENT_LIMIT=10
MDCLOUD_COMMENT_WINDOW=10m
EOF
  chmod 600 "$ENV_FILE"
  say "записал $ENV_FILE"
else
  say "читаю настройки из $ENV_FILE"
fi

set -a; . "$ENV_FILE"; set +a
DB_USER="${DB_USER:-mdcloud}"
DB_NAME="${DB_NAME:-mdcloud}"
DB_HOST="${DB_HOST:-localhost}"
DB_PORT="${DB_PORT:-5432}"
DB_PASSWORD="${DB_PASSWORD:-}"
# Кавычка в пароле не должна ломать psql-команду.
DB_PASSWORD_SQL="${DB_PASSWORD//\'/\'\'}"
[[ -n "${MDCLOUD_DATABASE_URL:-}" ]] || die "в $ENV_FILE нет MDCLOUD_DATABASE_URL"

echo "    каталог:  $APP_DIR"
echo "    сервис:   $SERVICE_NAME (пользователь $SERVICE_USER)"
echo "    облако:   ${MDCLOUD_BASE_URL:-?}"
echo "    редактор: ${MDCLOUD_EDITOR_URL:-?}"
echo "    база:     $DB_NAME (роль $DB_USER @ $DB_HOST:$DB_PORT)"
echo "    статика:  $STATIC_DEST"

# ── 2. Сборка ───────────────────────────────────────────────────────────────
find_go() {
  if [[ -n "${GO:-}" ]]; then echo "$GO"; return; fi
  local c
  for c in "$(command -v go 2>/dev/null || true)" /usr/local/go/bin/go \
           /usr/lib/go-*/bin/go "$HOME/go-root/bin/go"; do
    if [[ -n "$c" && -x "$c" ]]; then
      echo "$c"
      return
    fi
  done
}
GO_BIN="$(find_go || true)"
if [[ -z "$GO_BIN" ]]; then
  warn "Go не найден — ставлю в ~/go-root (как в других деплоерах)"
  arch=$(uname -m)
  case "$arch" in aarch64|arm64) goarch=arm64 ;; x86_64) goarch=amd64 ;; *) die "нет сборки Go для $arch" ;; esac
  curl -fsSL -o /tmp/go-mdcloud.tgz "https://go.dev/dl/go1.27.0.linux-$goarch.tar.gz"
  mkdir -p "$HOME/go-root"
  tar -C "$HOME/go-root" --strip-components=1 -xzf /tmp/go-mdcloud.tgz
  rm -f /tmp/go-mdcloud.tgz
  GO_BIN="$HOME/go-root/bin/go"
fi
say "сборка ($GO_BIN)"
GOTOOLCHAIN=auto "$GO_BIN" build -o "$APP_DIR/$BIN_NAME" .
chmod 755 "$APP_DIR/$BIN_NAME"

# ── 3. Postgres ─────────────────────────────────────────────────────────────
psql_as_postgres() {
  # cd в /tmp: у postgres нет прав на /home/*, иначе psql ругается на каждый вызов.
  (cd /tmp && sudo -u postgres psql -v ON_ERROR_STOP=1 -qAt "$@")
}

say "роль '$DB_USER'"
if [[ "$(psql_as_postgres -c "SELECT 1 FROM pg_roles WHERE rolname='$DB_USER'")" != "1" ]]; then
  psql_as_postgres -c "CREATE ROLE $DB_USER LOGIN PASSWORD '$DB_PASSWORD_SQL'" >/dev/null
  echo "    создана"
else
  psql_as_postgres -c "ALTER ROLE $DB_USER WITH LOGIN PASSWORD '$DB_PASSWORD_SQL'" >/dev/null
  echo "    уже была, пароль синхронизирован"
fi

say "база '$DB_NAME'"
if [[ "$(psql_as_postgres -c "SELECT 1 FROM pg_database WHERE datname='$DB_NAME'")" != "1" ]]; then
  (cd /tmp && sudo -u postgres createdb -O "$DB_USER" "$DB_NAME")
  echo "    создана"
else
  echo "    уже была"
fi

# ── 4. systemd ──────────────────────────────────────────────────────────────
say "сервис $UNIT"
if [[ ! -f "$UNIT" ]]; then
  UNIT_TMP="$(mktemp)"
  cat > "$UNIT_TMP" <<EOF
[Unit]
Description=mdcloud — облако markdown-документов
After=network-online.target postgresql.service
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
WorkingDirectory=$APP_DIR
EnvironmentFile=$ENV_FILE
ExecStart=$APP_DIR/$BIN_NAME
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
  sudo install -m 644 -o root -g root "$UNIT_TMP" "$UNIT"
  rm -f "$UNIT_TMP"
  echo "    создан"
else
  echo "    уже есть — не трогаю"
fi
sudo systemctl daemon-reload
sudo systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true
# restart, а не start: на редеплое сервис уже запущен, и без явного
# перезапуска он остался бы крутиться старым бинарём.
sudo systemctl restart "$SERVICE_NAME"

# ── 5. Статика и Caddy ──────────────────────────────────────────────────────
if [[ "$WITH_CADDY" == "1" ]]; then
  say "статика в $STATIC_DEST"
  # /var/www/html помечен setgid-битом и группой caddy — файлы, созданные
  # здесь, сразу получают нужную группу, sudo для этого не нужен.
  if [[ -w "$(dirname "$STATIC_DEST")" ]]; then
    mkdir -p "$STATIC_DEST"
    rsync -a --no-owner --no-group --delete "$APP_DIR/web/" "$STATIC_DEST/"
  else
    sudo mkdir -p "$STATIC_DEST"
    sudo rsync -a --no-owner --no-group --delete --chown="$SERVICE_USER":caddy \
      "$APP_DIR/web/" "$STATIC_DEST/"
  fi

  DOMAIN="$(echo "${MDCLOUD_BASE_URL:-}" | sed -e 's|^https\?://||' -e 's|/$||')"
  if [[ -z "$DOMAIN" ]]; then
    warn "в .env нет MDCLOUD_BASE_URL — Caddy не настраиваю"
  elif [[ ! -f "$CADDYFILE" ]]; then
    warn "$CADDYFILE не найден — Caddy не настраиваю"
  elif grep -qE "^[[:space:]]*${DOMAIN//./\\.}[[:space:]]*\{" "$CADDYFILE"; then
    echo "    сайт $DOMAIN в Caddyfile уже есть — не трогаю"
  else
    say "добавляю сайт $DOMAIN в $CADDYFILE"
    sudo cp -a "$CADDYFILE" "${CADDYFILE}.bak.$(date +%s)"
    BLOCK_TMP="$(mktemp)"
    cat > "$BLOCK_TMP" <<EOF

# --- $SERVICE_NAME (добавлено deploy.sh) ---
$DOMAIN {
	encode zstd gzip

	header {
		X-Content-Type-Options nosniff
		Referrer-Policy no-referrer
		Strict-Transport-Security "max-age=31536000"
		Content-Security-Policy "default-src 'self'; script-src 'self' https://cdn.jsdelivr.net; style-src 'self'; img-src 'self' data: https:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
	}

	handle /api/* {
		reverse_proxy ${MDCLOUD_ADDR:-127.0.0.1:8080}
	}

	handle {
		root * $STATIC_DEST
		try_files {path} /index.html
		file_server
	}
}
EOF
    sudo tee -a "$CADDYFILE" < "$BLOCK_TMP" >/dev/null
    rm -f "$BLOCK_TMP"
    if sudo caddy validate --config "$CADDYFILE" >/dev/null 2>&1; then
      sudo systemctl reload caddy
      echo "    конфиг проверен, Caddy перечитан (копия прежнего: ${CADDYFILE}.bak.*)"
    else
      # Откат: без валидного конфига Caddy не поднимется после перезапуска.
      sudo cp -a "$(ls -t "${CADDYFILE}".bak.* | head -1)" "$CADDYFILE"
      die "Caddy не принял конфиг — вернул прежний файл, смотри 'sudo caddy validate'"
    fi
  fi
fi

# ── Итог ────────────────────────────────────────────────────────────────────
sleep 1
sudo systemctl --no-pager --lines=10 status "$SERVICE_NAME" || true
echo
say "готово"
echo "    сервис:  systemctl status $SERVICE_NAME"
echo "    логи:    journalctl -u $SERVICE_NAME -f"
echo "    API:     ${MDCLOUD_BASE_URL:-http://$MDCLOUD_ADDR}/api/health"
if [[ -n "${MDCLOUD_BASE_URL:-}" ]]; then
  echo
  echo "    Первый вход: ${MDCLOUD_BASE_URL} — заведи себе аккаунт, он и станет"
  echo "    хозяином облака (регистрация закрыта, если ALLOW_REGISTRATION=false)."
fi
