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
	mux.HandleFunc("GET /api/config", s.publicConfig)
	mux.HandleFunc("GET /api/llm.md", s.llmGuideHandler)

	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.requireUser(s.logout))
	mux.HandleFunc("GET /api/me", s.requireUser(s.me))

	mux.HandleFunc("GET /api/docs", s.requireUser(s.listMine))
	mux.HandleFunc("POST /api/docs", s.requireUser(s.postDoc))
	mux.HandleFunc("GET /api/docs/{owner}", s.listByOwner)
	mux.HandleFunc("GET /api/docs/{owner}/{path...}", s.getDoc)
	mux.HandleFunc("PUT /api/docs/{owner}/{path...}", s.requireUser(s.putDoc))
	mux.HandleFunc("PATCH /api/docs/{owner}/{path...}", s.requireUser(s.moveDoc))
	mux.HandleFunc("DELETE /api/docs/{owner}/{path...}", s.requireUser(s.deleteDoc))

	mux.HandleFunc("GET /api/tokens", s.requireUser(s.listTokens))
	mux.HandleFunc("POST /api/tokens", s.requireUser(s.createToken))
	mux.HandleFunc("DELETE /api/tokens/{id}", s.requireUser(s.deleteToken))

	mux.HandleFunc("GET /api/comments/{owner}/{path...}", s.listComments)
	mux.HandleFunc("POST /api/comments/{owner}/{path...}", s.postComment)
	mux.HandleFunc("DELETE /api/comments/{id}", s.requireUser(s.deleteComment))

	mux.HandleFunc("GET /api/invites", s.requireAdmin(s.listInvites))
	mux.HandleFunc("POST /api/invites", s.requireAdmin(s.createInvite))
	mux.HandleFunc("DELETE /api/invites/{id}", s.requireAdmin(s.deleteInvite))

	// Локальная разработка: отдать web/ напрямую, чтобы не поднимать Caddy.
	if dir := strings.TrimSpace(os.Getenv("MDCLOUD_STATIC_DIR")); dir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(dir)))
	}

	return s.recoverer(s.cors(s.csrf(s.staticHeaders(mux))))
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
//
// Origin отражается точным значением (никаких звёздочек — с ними браузер
// запрещает отдавать куку), а Allow-Credentials нужен потому, что сессия
// ездит кукой: редактор и облако — два разных origin одного сайта.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			w.Header().Add("Vary", "Origin")
			if s.cfg.OriginAllowed(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
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

// csrf закрывает то, что открывает кука: браузер шлёт её сам, поэтому
// чужой сайт мог бы дёрнуть наш API «от имени» пользователя.
//
// Правило простое и без токенов в разметке: запрос, который меняет данные
// и пришёл с кукой сессии, обязан принести Origin нашего сайта. Браузер
// ставит Origin на все POST/PUT/DELETE — и на свои, и на чужие, — так что
// подделка из чужой вкладки отсекается здесь, а не в каждом обработчике.
// Скрипты с Authorization: Bearer под это правило не попадают: куки у них
// нет, а сам заголовок чужой сайт выставить не может.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch:
			// Форма с чужого сайта умеет только простые типы содержимого.
			// JSON она отправить не может: на него браузер сперва спросит
			// разрешение, а чужому сайту CORS его не даёт. DELETE формы не
			// умеют вовсе, поэтому его отдельно проверять нечем.
			ct := r.Header.Get("Content-Type")
			if r.ContentLength != 0 && !strings.HasPrefix(ct, "application/json") {
				writeErr(w, http.StatusUnsupportedMediaType,
					"тело запроса должно быть application/json")
				return
			}
		case http.MethodDelete:
		default:
			next.ServeHTTP(w, r)
			return
		}
		// Дальше — только про куку: её браузер прикладывает сам, значит
		// запрос обязан прийти с нашего сайта.
		if s.sessionCookie(r) == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.cfg.OriginAllowed(r.Header.Get("Origin")) {
			writeErr(w, http.StatusForbidden,
				"запрос с чужого сайта отклонён — обновите страницу и попробуйте снова")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- авторизация

// authenticate ищет, кому принадлежит запрос: сначала по куке, потом по
// заголовку Authorization.
//
// Кука — путь браузера: httpOnly, скрипту токен не виден. Bearer — путь
// скриптов и ассистентов: короткая сессия (её выдают входом) или постоянный
// API-ключ из меню учётной записи. Ключ и сессия живут в разных таблицах, но
// наружу это не видно: правила доступа дальше одинаковые.
func (s *Server) authenticate(r *http.Request) *models.User {
	if tok := s.sessionCookie(r); tok != "" {
		if u := s.userBySession(tok); u != nil {
			return u
		}
	}
	tok := bearerToken(r)
	if tok == "" {
		return nil
	}
	if u := s.userBySession(tok); u != nil {
		return u
	}
	return s.userByAPIToken(tok)
}

// userBySession узнаёт сессию по её токену.
func (s *Server) userBySession(tok string) *models.User {
	var sess models.Session
	err := s.db.Where("token_hash = ? AND expires_at > ?",
		auth.HashToken(tok), time.Now()).First(&sess).Error
	if err != nil {
		return nil
	}
	return s.userByID(sess.UserID)
}

// userByAPIToken узнаёт постоянный ключ и отмечает, что им пользовались.
//
// Отметку «последний раз» пишем не каждый раз: ключ может дёргать скрипт по
// расписанию, и лишний UPDATE на каждый запрос никому не нужен. Часа хватает,
// чтобы в списке было видно, живой ключ или забытый.
func (s *Server) userByAPIToken(tok string) *models.User {
	var key models.APIToken
	err := s.db.Where("token_hash = ?", auth.HashToken(tok)).First(&key).Error
	if err != nil {
		return nil
	}
	now := time.Now()
	if key.Expired(now) {
		return nil
	}
	if key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) > time.Hour {
		if err := s.db.Model(&key).Update("last_used_at", now).Error; err != nil {
			log.Printf("отметка использования ключа %d: %v", key.ID, err)
		}
	}
	return s.userByID(key.UserID)
}

func (s *Server) userByID(id uint) *models.User {
	var u models.User
	if err := s.db.First(&u, id).Error; err != nil {
		return nil
	}
	return &u
}

// sessionCookie читает куку сессии.
func (s *Server) sessionCookie(r *http.Request) string {
	c, err := r.Cookie(s.cfg.CookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	return c.Value
}

// setSessionCookie отдаёт браузеру токен входа.
//
// HttpOnly — JS токен не прочитает; Secure — только по https; SameSite=Lax
// — по чужому сайту кука не уедет, а по нашим поддоменам (редактор) уедет,
// потому что это один сайт. Домен родительский: вход один на оба сайта.
func (s *Server) setSessionCookie(w http.ResponseWriter, tok string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    tok,
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		Expires:  exp,
		MaxAge:   int(time.Until(exp).Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie убирает куку — при выходе и когда сессия протухла.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    "",
		Path:     "/",
		Domain:   s.cfg.CookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

type authedHandler func(w http.ResponseWriter, r *http.Request, u *models.User)

// requireUser пускает дальше только с живой сессией.
func (s *Server) requireUser(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := s.authenticate(r)
		if u == nil {
			// Кука с протухшей сессией только мешает: браузер будет слать
			// её в каждый запрос, а вход всё равно нужен заново.
			if s.sessionCookie(r) != "" {
				s.clearSessionCookie(w)
			}
			writeErr(w, http.StatusUnauthorized, "нужен вход")
			return
		}
		next(w, r, u)
	}
}

// requireAdmin пускает только хозяина облака и тех, кого он назначил.
func (s *Server) requireAdmin(next authedHandler) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request, u *models.User) {
		if !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "это действие доступно только хозяину облака")
			return
		}
		next(w, r, u)
	})
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
	sess := models.Session{TokenHash: hash, UserID: userID, ExpiresAt: exp}
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
