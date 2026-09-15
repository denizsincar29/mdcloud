// Package config собирает настройки сервиса mdcloud из окружения.
//
// Ни одного секрета в репозитории: DSN, соль для хеширования IP и адреса
// приходят из EnvironmentFile systemd-юнита (см. deploy/mdcloud.env.example).
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config — полный набор настроек одного процесса mdcloud.
type Config struct {
	Addr              string        // адрес прослушивания (за Caddy — только localhost)
	DatabaseURL       string        // postgres DSN
	BaseURL           string        // публичный адрес облака, https://mdcloud.example
	EditorURL         string        // публичный адрес редактора mathmd
	AllowedOrigins    []string      // кому разрешён CORS к /api (сайты редактора/облака)
	AllowRegistration bool          // открытая регистрация без приглашения
	CookieName        string        // имя куки сессии
	CookieDomain      string        // домен куки: пусто — только этот хост
	CookieSecure      bool          // слать куку только по https (в бою — да)
	SessionTTL        time.Duration // срок жизни токена сессии
	InviteTTL         time.Duration // срок жизни приглашения по умолчанию
	IPSalt            string        // соль для хеша IP: сырые адреса в БД не пишем
	CommentLimit      int           // сколько комментариев с одного IP за окно
	CommentWindow     time.Duration // длина окна для CommentLimit
	MaxDocBytes       int           // максимальный размер markdown-документа
}

// Load читает окружение и валидирует обязательные поля.
func Load() (*Config, error) {
	c := &Config{
		Addr:              env("MDCLOUD_ADDR", "127.0.0.1:8080"),
		DatabaseURL:       firstNonEmpty(os.Getenv("MDCLOUD_DATABASE_URL"), os.Getenv("DATABASE_URL")),
		BaseURL:           env("MDCLOUD_BASE_URL", ""),
		EditorURL:         env("MDCLOUD_EDITOR_URL", "https://mathmd.denizsincar.ru"),
		AllowedOrigins:    splitList(env("MDCLOUD_ALLOWED_ORIGINS", "")),
		AllowRegistration: envBool("MDCLOUD_ALLOW_REGISTRATION", false),
		CookieName:        env("MDCLOUD_COOKIE_NAME", "mdcloud_sid"),
		CookieDomain:      strings.TrimPrefix(env("MDCLOUD_COOKIE_DOMAIN", ""), "."),
		SessionTTL:        envDur("MDCLOUD_SESSION_TTL", 30*24*time.Hour),
		InviteTTL:         envDur("MDCLOUD_INVITE_TTL", 14*24*time.Hour),
		IPSalt:            env("MDCLOUD_IP_SALT", ""),
		CommentLimit:      envInt("MDCLOUD_COMMENT_LIMIT", 10),
		CommentWindow:     envDur("MDCLOUD_COMMENT_WINDOW", 10*time.Minute),
		MaxDocBytes:       envInt("MDCLOUD_MAX_DOC_BYTES", 2<<20),
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("MDCLOUD_DATABASE_URL (или DATABASE_URL) не задан")
	}
	if c.IPSalt == "" {
		return nil, fmt.Errorf("MDCLOUD_IP_SALT не задан — без него IP нечем хешировать")
	}
	if c.BaseURL == "" {
		c.BaseURL = "http://" + c.Addr
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	c.EditorURL = strings.TrimRight(c.EditorURL, "/")
	// Флаг Secure — следствие адреса, а не отдельная настройка: по http
	// браузер такую куку просто не примет, и вход молча сломается.
	c.CookieSecure = strings.HasPrefix(c.BaseURL, "https://")
	return c, nil
}

// originOf достаёт схему и хост из адреса — в таком виде его присылает
// браузер в заголовке Origin.
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// OriginAllowed сообщает, знаком ли нам этот Origin. Свой собственный сайт
// и сайт редактора считаются своими всегда — их не нужно перечислять в
// MDCLOUD_ALLOWED_ORIGINS.
func (c *Config) OriginAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	if origin == originOf(c.BaseURL) || origin == originOf(c.EditorURL) {
		return true
	}
	for _, o := range c.AllowedOrigins {
		if o != "" && o == origin {
			return true
		}
	}
	return false
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDur(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, strings.TrimRight(p, "/"))
		}
	}
	return out
}
