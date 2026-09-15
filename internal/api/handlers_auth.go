package api

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/denizsincar29/mdcloud/internal/auth"
	"github.com/denizsincar29/mdcloud/internal/mdpath"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// errInviteBad — приглашение не подошло: чужое, просроченное или уже
// использованное. Разбираться, какое именно, тому, кто регистрируется,
// незачем — и вредно: разные ответы подсказывали бы, какие коды бывают.
var errInviteBad = errors.New("приглашение не подошло")

// Политика обработки персональных данных: адрес для человека и редакция,
// которую он принимает. Согласие — правовое основание обработки (152-ФЗ),
// поэтому редакция сохраняется у аккаунта вместе со временем принятия.
const (
	policyURL      = "https://denizsincar.ru/privacy"
	policyRevision = "2026-09-15"
)

// userView — то, что можно показывать наружу. Хеш пароля не покидает сервер.
type userView struct {
	ID          uint   `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name"`
	IsAdmin     bool   `json:"is_admin"`
}

func viewUser(u *models.User) userView {
	return userView{
		ID:          u.ID,
		Username:    u.Username,
		Email:       u.EmailString(),
		DisplayName: u.DisplayName,
		IsAdmin:     u.IsAdmin,
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.lim.allow("reg:"+clientIP(r), 10, time.Hour) {
		writeErr(w, http.StatusTooManyRequests, "слишком много попыток регистрации, попробуйте позже")
		return
	}
	var in struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
		Invite      string `json:"invite"`
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
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email != "" && (!strings.Contains(email, "@") || len(email) > 255) {
		writeErr(w, http.StatusBadRequest, "адрес почты выглядит странно")
		return
	}

	var count int64
	if err := s.db.Model(&models.User{}).Count(&count).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	// Первый пользователь проходит всегда — он и есть хозяин облака, ему
	// приглашение выдать некому.
	first := count == 0
	code := strings.TrimSpace(in.Invite)
	if !first && !s.cfg.AllowRegistration && code == "" {
		writeErr(w, http.StatusForbidden, "регистрация по приглашению: нужен код")
		return
	}
	if s.userExists(username, email) {
		writeErr(w, http.StatusConflict, "такое имя или почта уже заняты")
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
		IsAdmin:       first, // хозяин облака: он выдаёт приглашения
		ConsentAt:     &now,
		ConsentPolicy: policyRevision,
	}
	if email != "" {
		u.Email = &email
	}

	if err := s.createUser(&u, code, first); err != nil {
		switch {
		case errors.Is(err, errInviteBad):
			writeErr(w, http.StatusForbidden,
				"приглашение не подошло: оно использовано, просрочено или выписано не здесь")
		case isDuplicate(err):
			writeErr(w, http.StatusConflict, "такое имя или почта уже заняты")
		default:
			log.Printf("создание пользователя %s: %v", username, err)
			writeErr(w, http.StatusInternalServerError, "не смог создать пользователя")
		}
		return
	}

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

// createUser заводит пользователя, а вместе с ним сжигает приглашение —
// одной транзакцией.
//
// Порядок важен: сначала проверяем и запираем приглашение, потом создаём
// пользователя. Если создание не сложилось (занятое имя, обрыв базы),
// транзакция откатится и код останется рабочим — человек попробует снова,
// а не пойдёт просить новое приглашение.
func (s *Server) createUser(u *models.User, code string, first bool) error {
	if first || code == "" {
		return s.db.Create(u).Error
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var inv models.Invite
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("code_hash = ? AND used_at IS NULL AND expires_at > ?",
				auth.HashToken(code), time.Now()).
			First(&inv).Error
		if err != nil {
			return errInviteBad
		}
		if err := tx.Create(u).Error; err != nil {
			return err
		}
		now := time.Now()
		res := tx.Model(&models.Invite{}).
			Where("id = ? AND used_at IS NULL", inv.ID).
			Updates(map[string]any{"used_by": u.ID, "used_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errInviteBad
		}
		return nil
	})
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

// publicConfig — то, что странице входа нужно знать до входа: можно ли
// регистрироваться и нужен ли для этого код.
func (s *Server) publicConfig(w http.ResponseWriter, r *http.Request) {
	var users, invites int64
	if err := s.db.Model(&models.User{}).Count(&users).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	if err := s.db.Model(&models.Invite{}).
		Where("used_at IS NULL AND expires_at > ?", time.Now()).Count(&invites).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	mode := "closed"
	switch {
	case users == 0:
		mode = "first" // место хозяина свободно
	case s.cfg.AllowRegistration:
		mode = "open"
	case invites > 0:
		mode = "invite"
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
	if !s.lim.allow("login:"+clientIP(r), 20, 10*time.Minute) {
		writeErr(w, http.StatusTooManyRequests, "слишком много попыток входа, подождите немного")
		return
	}

	login := strings.ToLower(strings.TrimSpace(in.Login))
	var u models.User
	err := s.db.Where("username = ? OR email = ?", login, login).First(&u).Error
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

func (s *Server) userExists(username, email string) bool {
	var n int64
	q := s.db.Model(&models.User{}).Where("username = ?", username)
	if email != "" {
		q = q.Or("email = ?", email)
	}
	q.Count(&n)
	return n > 0
}
