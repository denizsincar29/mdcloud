// Package config собирает настройки сервиса mdcloud из окружения.
//
// Ни одного секрета в репозитории: DSN, соль для хеширования IP и адреса
// приходят из EnvironmentFile systemd-юнита (см. deploy/mdcloud.env.example).
package config

import (
	"fmt"
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
	AllowRegistration bool          // открыта ли самостоятельная регистрация
	SessionTTL        time.Duration // срок жизни токена сессии
	HandoffTTL        time.Duration // срок жизни одноразового кода перехода в редактор
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
		AllowRegistration: envBool("MDCLOUD_ALLOW_REGISTRATION", true),
		SessionTTL:        envDur("MDCLOUD_SESSION_TTL", 30*24*time.Hour),
		HandoffTTL:        envDur("MDCLOUD_HANDOFF_TTL", 90*time.Second),
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
	return c, nil
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
