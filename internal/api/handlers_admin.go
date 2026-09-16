package api

// Хозяйское: учётные записи облака.
//
// Единственное, что здесь нельзя, — остаться без хозяина. Держится это на
// одном отказе: себя не удаляют и не разжаловывают. Отсюда инвариант — кто
// правит учётные записи, тот сам хозяин, а значит, хозяин в облаке есть
// всегда, пока он есть хоть у кого-то. Отдельной проверки «это последний
// хозяин» не нужно: чтобы дотянуться до чужой учётки, надо быть хозяином,
// то есть вторым.
//
// Регистрация открыта всем, поэтому вход в облако ничего не стоит — и
// единственное, что удерживает его от превращения в свалку, это хозяин,
// который видит, кто пришёл, и может учётку убрать. Здесь ровно это: список
// аккаунтов и три действия над ними — выгнать, разжаловать, удалить.
//
// Приглашений в этой картине нет вовсе: код на входе был пропуском в
// закрытое облако, а закрытого облака больше нет.

import (
	"net/http"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/denizsincar29/mdcloud/internal/models"
)

// accountView — аккаунт в списке хозяина. Лишнего не показываем: ни хеша
// пароля, ни сессий — только то, по чему решают, оставлять человека или нет.
type accountView struct {
	ID          uint       `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	IsAdmin     bool       `json:"is_admin"`
	CreatedAt   time.Time  `json:"created_at"`
	Docs        int64      `json:"docs"`
	LastLogin   *time.Time `json:"last_login,omitempty"`
	Me          bool       `json:"me"`
}

// listAccounts — GET /api/admin/users: все аккаунты, кто раньше — выше.
//
// Документы и время последнего входа считаем отдельными запросами с
// группировкой, а не по строке на человека: аккаунтов немного, но N+1
// запросов на список — это привычка, которая переживёт и рост.
func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request, u *models.User) {
	var users []models.User
	if err := s.db.Order("id ASC").Find(&users).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}

	docs := map[uint]int64{}
	var docRows []struct {
		OwnerID uint
		N       int64
	}
	if err := s.db.Model(&models.Doc{}).Select("owner_id, count(*) AS n").
		Group("owner_id").Scan(&docRows).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	for _, row := range docRows {
		docs[row.OwnerID] = row.N
	}

	// Последний вход — самая свежая сессия. Точнее было бы отмечать каждый
	// заход, но это лишняя запись на каждый запрос, а для «заходил ли он
	// вообще» хватает и этого.
	seen := map[uint]time.Time{}
	var sessRows []struct {
		UserID    uint
		CreatedAt time.Time
	}
	if err := s.db.Model(&models.Session{}).Select("user_id, max(created_at) AS created_at").
		Group("user_id").Scan(&sessRows).Error; err == nil {
		for _, row := range sessRows {
			seen[row.UserID] = row.CreatedAt
		}
	}

	out := make([]accountView, 0, len(users))
	for i := range users {
		v := accountView{
			ID:          users[i].ID,
			Username:    users[i].Username,
			DisplayName: users[i].DisplayName,
			IsAdmin:     users[i].IsAdmin,
			CreatedAt:   users[i].CreatedAt,
			Docs:        docs[users[i].ID],
			Me:          users[i].ID == u.ID,
		}
		if at, ok := seen[users[i].ID]; ok {
			v.LastLogin = &at
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// deleteAccount — DELETE /api/admin/users/{id}: стереть учётку вместе со всем,
// что человек написал.
//
// Удаление жёсткое, а не «в корзину»: обещание «учётки больше нет» должно
// значить именно это. Документы уходят вместе с комментариями, сессиями и
// ключами — иначе чужой ключ пережил бы своего хозяина.
func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request, u *models.User) {
	target, ok := s.accountFromPath(w, r)
	if !ok {
		return
	}
	if target.ID == u.ID {
		writeErr(w, http.StatusBadRequest, "себя удалять нечем — это ваша учётка")
		return
	}
	if err := s.wipeAccount(target); err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог удалить учётную запись")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "username": target.Username})
}

// setAccountAdmin — POST /api/admin/users/{id}/admin {"admin": true|false}.
func (s *Server) setAccountAdmin(w http.ResponseWriter, r *http.Request, u *models.User) {
	target, ok := s.accountFromPath(w, r)
	if !ok {
		return
	}
	in := struct {
		Admin *bool `json:"admin"`
	}{}
	if !decodeJSON(w, r, &in, 1024) {
		return
	}
	if in.Admin == nil {
		writeErr(w, http.StatusBadRequest, "не понял, назначать или разжаловать")
		return
	}
	if !*in.Admin && target.ID == u.ID {
		writeErr(w, http.StatusBadRequest,
			"себя разжаловать нельзя: хозяин — тот, кто ведёт учётные записи")
		return
	}
	if err := s.db.Model(&models.User{}).Where("id = ?", target.ID).
		Update("is_admin", *in.Admin).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог изменить права")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "is_admin": *in.Admin})
}

// kickAccount — POST /api/admin/users/{id}/logout: выгнать из всех сессий и
// отозвать все ключи.
//
// И то и другое сразу: разговор всегда один и тот же — «этот аккаунт больше
// не должен ходить в облако». Оставить ключи живыми значило бы выгнать
// человека из браузера и оставить ему скрипт.
func (s *Server) kickAccount(w http.ResponseWriter, r *http.Request, u *models.User) {
	target, ok := s.accountFromPath(w, r)
	if !ok {
		return
	}
	sessions := s.db.Where("user_id = ?", target.ID).Delete(&models.Session{})
	tokens := s.db.Where("user_id = ?", target.ID).Delete(&models.APIToken{})
	if sessions.Error != nil || tokens.Error != nil {
		writeErr(w, http.StatusInternalServerError, "не смог закрыть доступ")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"sessions": sessions.RowsAffected,
		"tokens":   tokens.RowsAffected,
	})
}

// accountFromPath достаёт аккаунт по номеру из адреса.
func (s *Server) accountFromPath(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "не понял, о какой учётной записи речь")
		return nil, false
	}
	var target models.User
	if err := s.db.First(&target, uint(id)).Error; err != nil {
		writeErr(w, http.StatusNotFound, "такой учётной записи нет")
		return nil, false
	}
	return &target, true
}

// wipeAccount стирает всё, что осталось от аккаунта, одной транзакцией:
// половина удалённого человека хуже, чем целый.
func (s *Server) wipeAccount(u *models.User) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var docs []models.Doc
		if err := tx.Unscoped().Select("id").Where("owner_id = ?", u.ID).
			Find(&docs).Error; err != nil {
			return err
		}
		ids := make([]uint, 0, len(docs))
		for i := range docs {
			ids = append(ids, docs[i].ID)
		}
		if len(ids) > 0 {
			if err := tx.Where("doc_id IN ?", ids).Delete(&models.Comment{}).Error; err != nil {
				return err
			}
			if err := tx.Where("doc_id IN ?", ids).Delete(&models.DocShare{}).Error; err != nil {
				return err
			}
			// Документ удаляем жёстко: мягкое удаление оставило бы содержимое
			// в таблице, а учётка должна уйти целиком.
			if err := tx.Unscoped().Where("id IN ?", ids).Delete(&models.Doc{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("author_id = ?", u.ID).Delete(&models.Comment{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", u.ID).Delete(&models.DocShare{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", u.ID).Delete(&models.Session{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", u.ID).Delete(&models.APIToken{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(&models.User{}, u.ID).Error
	})
}
