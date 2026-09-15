package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/denizsincar29/mdcloud/internal/mdpath"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// Отправка документа человеку: хозяин пишет файл и отдаёт его адресату по
// юзернейму, не выкладывая в публичный доступ. Получателю — чтение и
// комментарии; правка остаётся за хозяином, отправленный документ не
// превращается в общий.
//
// Адрес документа и юзернейм едут в теле или в строке запроса, а не в пути:
// у маршрута /api/docs/{owner}/{path...} путь — хвостовой, ничего после него
// в шаблоне стоять не может. Плюс так один и тот же вид запроса годится и
// для отправки, и для отзыва.

// shareInput — что нужно, чтобы отправить документ: чей он, какой и кому.
type shareInput struct {
	Owner    string `json:"owner"`
	Path     string `json:"path"`
	Username string `json:"username"`
}

// fromRequest собирает вход из тела JSON или из строки запроса. DELETE с телом
// проходит не везде, поэтому отзыв ходит параметрами — обе формы должны
// разбираться одинаково.
func (in *shareInput) fromRequest(w http.ResponseWriter, r *http.Request) bool {
	q := r.URL.Query()
	if v := q.Get("owner"); v != "" {
		in.Owner = v
	}
	if v := q.Get("path"); v != "" {
		in.Path = v
	}
	if v := q.Get("username"); v != "" {
		in.Username = v
	}
	// Тело читаем, только если оно есть: у отзыва его может не быть вовсе.
	if r.ContentLength != 0 {
		var body shareInput
		if !decodeJSON(w, r, &body, 4096) {
			return false
		}
		if body.Owner != "" {
			in.Owner = body.Owner
		}
		if body.Path != "" {
			in.Path = body.Path
		}
		if body.Username != "" {
			in.Username = body.Username
		}
	}
	return true
}

// shareNames — кому документ отправлен, по юзернеймам.
func (s *Server) shareNames(docID uint) ([]string, error) {
	names := []string{}
	err := s.db.Table("doc_shares").
		Joins("JOIN users ON users.id = doc_shares.user_id").
		Where("doc_shares.doc_id = ?", docID).
		Order("users.username").
		Pluck("users.username", &names).Error
	return names, err
}

// sharedWith сообщает, отправлен ли документ этому человеку.
func (s *Server) sharedWith(docID, userID uint) bool {
	var n int64
	if err := s.db.Model(&models.DocShare{}).
		Where("doc_id = ? AND user_id = ?", docID, userID).Count(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// myDoc достаёт документ запроса и убеждается, что он принадлежит этому
// человеку: отправлять и забирать документ может только хозяин. Приватный
// чужой документ так же отвечает 404, как и везде, — существование закрытых
// записей не подтверждаем.
func (s *Server) myDoc(w http.ResponseWriter, r *http.Request, u *models.User, in *shareInput) (*models.Doc, *models.User, bool) {
	owner, ok := s.findUser(in.Owner)
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return nil, nil, false
	}
	path, err := mdpath.Normalize(in.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, nil, false
	}
	doc, err := s.findDoc(owner.ID, path)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return nil, nil, false
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return nil, nil, false
	}
	// Публичный документ открыт всем и без отправки, а личный — только хозяину.
	if owner.ID != u.ID {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return nil, nil, false
	}
	return doc, owner, true
}

// shareDoc — POST /api/share: отправить документ человеку по юзернейму.
func (s *Server) shareDoc(w http.ResponseWriter, r *http.Request, u *models.User) {
	var in shareInput
	if !in.fromRequest(w, r) {
		return
	}
	doc, _, ok := s.myDoc(w, r, u, &in)
	if !ok {
		return
	}
	target, ok := s.findUser(strings.TrimSpace(in.Username))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	if target.ID == u.ID {
		writeErr(w, http.StatusBadRequest, "документ и так ваш")
		return
	}
	// Повторная отправка тому же человеку ничего не меняет: строка уже есть,
	// и второй такой же быть не должно (уникальный индекс doc_id + user_id).
	share := models.DocShare{DocID: doc.ID, UserID: target.ID}
	if err := s.db.Where(share).FirstOrCreate(&share).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	s.respondShareNames(w, doc, target.Username, "Документ отправлен: "+target.Name()+".")
}

// unshareDoc — DELETE /api/share: забрать документ обратно.
func (s *Server) unshareDoc(w http.ResponseWriter, r *http.Request, u *models.User) {
	var in shareInput
	if !in.fromRequest(w, r) {
		return
	}
	doc, _, ok := s.myDoc(w, r, u, &in)
	if !ok {
		return
	}
	target, ok := s.findUser(strings.TrimSpace(in.Username))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	if err := s.db.Where("doc_id = ? AND user_id = ?", doc.ID, target.ID).
		Delete(&models.DocShare{}).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	s.respondShareNames(w, doc, target.Username, "Документ больше не у "+target.Name()+".")
}

func (s *Server) respondShareNames(w http.ResponseWriter, doc *models.Doc, username, message string) {
	names, err := s.shareNames(doc.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"shared_with": names,
		"username":    username,
		"message":     message,
	})
}

// listSharedWithMe — GET /api/shared: документы, которые отправили мне.
//
// Отдаём отдельным списком, а не подмешиваем в «мои документы»: там владение
// и право правки, а тут чужая работа, которую мне дали почитать.
func (s *Server) listSharedWithMe(w http.ResponseWriter, r *http.Request, u *models.User) {
	var docs []models.Doc
	err := s.db.
		Joins("JOIN doc_shares ON doc_shares.doc_id = docs.id").
		Where("doc_shares.user_id = ?", u.ID).
		Where("docs.expires_at IS NULL OR docs.expires_at > ?", time.Now()).
		Preload("Owner").
		Order("docs.updated_at DESC").
		Find(&docs).Error
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	out := make([]docView, 0, len(docs))
	for i := range docs {
		out = append(out, s.viewDoc(&docs[i], docs[i].Owner.Username, u, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": out})
}
