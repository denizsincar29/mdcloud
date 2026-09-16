package api

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/denizsincar29/mdcloud/internal/auth"
	"github.com/denizsincar29/mdcloud/internal/mdpath"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// Политика обработки персональных данных: адрес для человека и редакция,
// которую он принимает. Согласие — правовое основание обработки (152-ФЗ),
// поэтому редакция сохраняется у аккаунта вместе со временем принятия.
const (
	policyURL      = "https://denizsincar.ru/privacy"
	policyRevision = "2026-09-15"
)

// Пределы на попытки входа и регистрации — рядом с окном, которое уходит в
// Retry-After: разъехавшись, они соврали бы человеку про «подождите немного».
const (
	registerLimit = 10
	registerTries = time.Hour
	loginLimit    = 20
	loginTries    = 10 * time.Minute
)

// userView — то, что можно показывать наружу. Хеш пароля не покидает сервер.
// И почты здесь нет: её у аккаунта больше нет.
type userView struct {
	ID          uint   `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	IsAdmin     bool   `json:"is_admin"`
}

func viewUser(u *models.User) userView {
	return userView{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		IsAdmin:     u.IsAdmin,
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
}

// register заводит аккаунт. Регистрация открыта всем: пропусков в облако
// больше нет, а порядок держит хозяин — он видит нового человека (уведомление
// в ntfy, список учётных записей) и убирает учётку, если она ни к чему.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.lim.allow("reg:"+clientIP(r), registerLimit, registerTries) {
		retryAfter(w, registerTries)
		writeErr(w, http.StatusTooManyRequests, "слишком много попыток регистрации, попробуйте позже")
		return
	}
	var in struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Consent     bool   `json:"consent"`
	}
	if !decodeJSON(w, r, &in, 4096) {
		return
	}
	// Без согласия аккаунт не заводим: обработка начинается с первого
	// записанного поля, а подтвердить согласие будет нечем.
	if !in.Consent {
		writeErr(w, http.StatusBadRequest,
			"нужно согласие с политикой обработки персональных данных: "+policyURL)
		return
	}

	username := mdpath.CanonicalUsername(in.Username)
	if !mdpath.ValidUsername(username) {
		writeErr(w, http.StatusBadRequest,
			"имя: 3–32 символа, латиница в нижнем регистре, цифры, дефис и подчёркивание")
		return
	}
	if len(in.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "пароль короче восьми символов")
		return
	}

	var count int64
	if err := s.db.Model(&models.User{}).Count(&count).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	// Первый пользователь — хозяин облака: он ведёт учётные записи, и
	// назначить его, кроме этой строки, некому.
	first := count == 0
	if s.userExists(username) {
		writeErr(w, http.StatusConflict, "такое имя уже занято")
		return
	}

	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог захешировать пароль")
		return
	}
	now := time.Now().UTC()
	u := models.User{
		Username:      username,
		PasswordHash:  hash,
		DisplayName:   strings.TrimSpace(in.DisplayName),
		IsAdmin:       first, // хозяин облака: он ведёт учётные записи
		ConsentAt:     &now,
		ConsentPolicy: policyRevision,
	}
	if err := s.db.Create(&u).Error; err != nil {
		if isDuplicate(err) {
			writeErr(w, http.StatusConflict, "такое имя уже занято")
		} else {
			log.Printf("создание пользователя %s: %v", username, err)
			writeErr(w, http.StatusInternalServerError, "не смог создать пользователя")
		}
		return
	}
	// Хозяин узнаёт о новом человеке сразу: регистрация открыта, и «кто-то
	// пришёл» — единственная новость, которую здесь стоит рассказывать.
	s.notify("Новый аккаунт в облаке",
		username+" зарегистрировался "+now.In(moscowTime()).Format("02.01.2006 15:04"))

	tok, exp, err := s.issueSession(u.ID, s.cfg.SessionTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выдать токен")
		return
	}
	s.setSessionCookie(w, tok, exp)
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      tok,
		"expires_at": exp,
		"user":       viewUser(&u),
	})
}

// moscowTime — пояс, в котором живёт хозяин: время в уведомлении должно
// читаться как «сейчас», а не пересчитываться в голове из UTC.
func moscowTime() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.UTC
	}
	return loc
}

// isDuplicate отличает «имя занято» от «база упала»: драйверы сообщают о
// нарушении уникальности разными словами, а внятный ответ человеку нужен.
func isDuplicate(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate key")
}

// publicConfig — то, что странице входа нужно знать до входа. Регистрация
// открыта всегда; разница только в том, свободно ли место хозяина.
func (s *Server) publicConfig(w http.ResponseWriter, r *http.Request) {
	var users int64
	if err := s.db.Model(&models.User{}).Count(&users).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	mode := "open"
	if users == 0 {
		mode = "first" // место хозяина свободно
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"registration": mode,
		"cloud":        s.cfg.BaseURL,
		"editor":       s.cfg.EditorURL,
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in, 4096) {
		return
	}
	if !s.lim.allow("login:"+clientIP(r), loginLimit, loginTries) {
		retryAfter(w, loginTries)
		writeErr(w, http.StatusTooManyRequests, "слишком много попыток входа, подождите немного")
		return
	}

	login := mdpath.CanonicalUsername(strings.TrimSpace(in.Login))
	var u models.User
	err := s.db.Where("username = ?", login).First(&u).Error
	if err != nil || !auth.CheckPassword(u.PasswordHash, in.Password) {
		// Одинаковая ошибка и небольшая задержка: не подсказываем, что
		// именно неверно — имя или пароль.
		time.Sleep(300 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "неверное имя или пароль")
		return
	}

	tok, exp, err := s.issueSession(u.ID, s.cfg.SessionTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выдать токен")
		return
	}
	s.setSessionCookie(w, tok, exp)
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      tok,
		"expires_at": exp,
		"user":       viewUser(&u),
	})
}

// logout гасит сессию, которой пришёл запрос: и куку, и строку в базе.
// Куку снимаем всегда, даже если токена в базе уже нет — иначе она будет
// висеть в браузере и путать следующий вход.
func (s *Server) logout(w http.ResponseWriter, r *http.Request, u *models.User) {
	tok := s.sessionCookie(r)
	if tok == "" {
		tok = bearerToken(r)
	}
	if tok != "" {
		s.db.Where("token_hash = ?", auth.HashToken(tok)).Delete(&models.Session{})
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request, u *models.User) {
	var docs int64
	s.db.Model(&models.Doc{}).Where("owner_id = ?", u.ID).Count(&docs)
	writeJSON(w, http.StatusOK, map[string]any{"user": viewUser(u), "docs": docs})
}

func (s *Server) userExists(username string) bool {
	var n int64
	s.db.Model(&models.User{}).Where("username = ?", username).Count(&n)
	return n > 0
}
