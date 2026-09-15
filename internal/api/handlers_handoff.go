package api

import (
	"errors"
	"net/http"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/denizsincar29/mdcloud/internal/auth"
	"github.com/denizsincar29/mdcloud/internal/mdpath"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// Переход облако → редактор.
//
// Куки между двумя сайтами не делятся (и не должны). Вместо этого облако
// выдаёт одноразовый код, кладёт его в URL-фрагмент и уводит браузер в
// mathmd. Фрагмент (#...) на сервер не отправляется — код не осядет ни в
// логах Caddy, ни в Referer. Редактор меняет код на обычный токен сессии
// и сразу чистит адрес.
//
// Код живёт меньше двух минут и сгорает при первом обмене: перехватить его
// можно только в том же браузере и в те же секунды.

// createHandoff — POST /api/handoff {"path": "ДЗ/ИИ/задачи"} → код и ссылка.
func (s *Server) createHandoff(w http.ResponseWriter, r *http.Request, u *models.User) {
	var in struct {
		Path string `json:"path"`
	}
	if !decodeJSON(w, r, &in, 4096) {
		return
	}
	path, err := mdpath.Normalize(in.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var doc models.Doc
	err = s.db.Where("owner_id = ? AND path = ?", u.ID, path).First(&doc).Error
	if err != nil {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return
	}

	code, hash, err := auth.NewCode()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выдать код перехода")
		return
	}
	exp := time.Now().Add(s.cfg.HandoffTTL)
	sess := models.Session{
		TokenHash: hash,
		UserID:    u.ID,
		Kind:      models.KindHandoff,
		DocPath:   doc.Path,
		ExpiresAt: exp,
	}
	if err := s.db.Create(&sess).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выдать код перехода")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"code":       code,
		"expires_at": exp,
		"path":       doc.Path,
		"owner":      u.Username,
		// Фрагмент: браузер его не отправляет на сервер.
		"url": s.cfg.EditorURL + "/#cloud=" + code,
	})
}

// redeemHandoff — POST /api/handoff/redeem {"code": "..."} → токен сессии.
func (s *Server) redeemHandoff(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if !decodeJSON(w, r, &in, 4096) {
		return
	}
	if !s.lim.allow("redeem:"+clientIP(r), 60, 10*time.Minute) {
		writeErr(w, http.StatusTooManyRequests, "слишком много попыток, подождите")
		return
	}

	// Обмен код→токен идёт в транзакции: строку читаем под блокировкой и
	// сразу удаляем. Два параллельных запроса с одним кодом не пройдут
	// оба — второй упрётся в RowsAffected = 0.
	var (
		userID  uint
		docPath string
	)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var sess models.Session
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("token_hash = ? AND kind = ? AND expires_at > ?",
				auth.HashToken(in.Code), models.KindHandoff, time.Now()).
			First(&sess).Error; err != nil {
			return err
		}
		res := tx.Where("token_hash = ? AND kind = ?", sess.TokenHash, models.KindHandoff).
			Delete(&models.Session{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errCodeSpent
		}
		userID, docPath = sess.UserID, sess.DocPath
		return nil
	})
	if err != nil {
		if errors.Is(err, errCodeSpent) || errors.Is(err, gorm.ErrRecordNotFound) {
			writeErr(w, http.StatusGone, "код перехода истёк или уже использован — откройте документ заново")
			return
		}
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}

	var u models.User
	if err := s.db.First(&u, userID).Error; err != nil {
		writeErr(w, http.StatusUnauthorized, "пользователь не найден")
		return
	}
	tok, exp, err := s.issueSession(u.ID, s.cfg.SessionTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог выдать токен")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      tok,
		"expires_at": exp,
		"user":       viewUser(&u),
		"owner":      u.Username,
		"path":       docPath,
	})
}

// errCodeSpent — код перехода уже забрали параллельным запросом.
var errCodeSpent = errors.New("код перехода уже использован")
