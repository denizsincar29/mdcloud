// Package api — HTTP-слой mdcloud: маршруты, авторизация, JSON.
//
// Сервер отдаёт только данные. Страницу предпросмотра раздаёт Caddy из
// web/ — так API можно перезапускать, не трогая статику, и наоборот.
package api

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/denizsincar29/mdcloud/internal/auth"
	"github.com/denizsincar29/mdcloud/internal/config"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// Server держит зависимости обработчиков.
type Server struct {
	cfg *config.Config
	db  *gorm.DB
	lim *limiter
}

// New собирает сервер.
func New(cfg *config.Config, db *gorm.DB) *Server {
	return &Server{cfg: cfg, db: db, lim: newLimiter()}
}

// Handler возвращает готовый http.Handler со всеми маршрутами.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)

	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.requireUser(s.logout))
	mux.HandleFunc("GET /api/me", s.requireUser(s.me))

	mux.HandleFunc("GET /api/docs", s.requireUser(s.listMine))
	mux.HandleFunc("GET /api/docs/{owner}", s.listByOwner)
	mux.HandleFunc("GET /api/docs/{owner}/{path...}", s.getDoc)
	mux.HandleFunc("PUT /api/docs/{owner}/{path...}", s.requireUser(s.putDoc))
	mux.HandleFunc("DELETE /api/docs/{owner}/{path...}", s.requireUser(s.deleteDoc))

	mux.HandleFunc("GET /api/comments/{owner}/{path...}", s.listComments)
	mux.HandleFunc("POST /api/comments/{owner}/{path...}", s.postComment)
	mux.HandleFunc("DELETE /api/comments/{id}", s.requireUser(s.deleteComment))

	mux.HandleFunc("POST /api/handoff", s.requireUser(s.createHandoff))
	mux.HandleFunc("POST /api/handoff/redeem", s.redeemHandoff)

	// Локальная разработка: отдать web/ напрямую, чтобы не поднимать Caddy.
	if dir := strings.TrimSpace(os.Getenv("MDCLOUD_STATIC_DIR")); dir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(dir)))
	}

	return s.recoverer(s.cors(s.staticHeaders(mux)))
}

// ---------------------------------------------------------------- middleware

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic %s %s: %v", r.Method, r.URL.Path, rec)
				writeErr(w, http.StatusInternalServerError, "внутренняя ошибка")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) staticHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// cors разрешает обращения к /api только с известных сайтов.
// Никаких Allow-Credentials: авторизация идёт заголовком Authorization,
// куки между облаком и редактором не разделяются вообще.
func (s *Server) cors(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(s.cfg.AllowedOrigins))
	for _, o := range s.cfg.AllowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Add("Vary", "Origin")
			if allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Max-Age", "600")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- авторизация

// authenticate ищет сессию по токену из Authorization: Bearer.
func (s *Server) authenticate(r *http.Request) *models.User {
	tok := bearerToken(r)
	if tok == "" {
		return nil
	}
	var sess models.Session
	err := s.db.Where("token_hash = ? AND kind = ? AND expires_at > ?",
		auth.HashToken(tok), models.KindSession, time.Now()).First(&sess).Error
	if err != nil {
		return nil
	}
	var u models.User
	if err := s.db.First(&u, sess.UserID).Error; err != nil {
		return nil
	}
	return &u
}

type authedHandler func(w http.ResponseWriter, r *http.Request, u *models.User)

// requireUser пускает дальше только с живой сессией.
func (s *Server) requireUser(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.authenticate(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "нужен вход")
			return
		}
		next(w, r, u)
	}
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

// issueSession заводит токен входа и возвращает его открытым текстом —
// в базе осядет только хеш.
func (s *Server) issueSession(userID uint, ttl time.Duration) (string, time.Time, error) {
	tok, hash, err := auth.NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(ttl)
	sess := models.Session{TokenHash: hash, UserID: userID, Kind: models.KindSession, ExpiresAt: exp}
	if err := s.db.Create(&sess).Error; err != nil {
		return "", time.Time{}, err
	}
	return tok, exp, nil
}

// ---------------------------------------------------------------- мелочи

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// decodeJSON читает тело запроса с ограничением размера.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any, maxBytes int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "не разобрал запрос: "+err.Error())
		return false
	}
	return true
}

// clientIP берёт адрес посетителя. За прокси (Caddy на localhost) доверяем
// X-Forwarded-For, иначе считаем по сокету.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
				return first
			}
		}
		if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
			return xr
		}
	}
	return host
}

// ---------------------------------------------------------------- лимитер

// limiter — окно на N запросов в памяти процесса. Для одного узла этого
// достаточно; при нескольких узлах его место займёт общий счётчик в БД.
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

// allow сообщает, укладывается ли очередной запрос в окно.
func (l *limiter) allow(key string, limit int, window time.Duration) bool {
	now := time.Now()
	cut := now.Add(-window)

	l.mu.Lock()
	defer l.mu.Unlock()

	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= limit {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	// Страховка от роста карты: раз в много запросов подчищаем пустые ключи.
	if len(l.hits) > 10000 {
		for k, v := range l.hits {
			if len(v) == 0 {
				delete(l.hits, k)
			}
		}
	}
	return true
}
