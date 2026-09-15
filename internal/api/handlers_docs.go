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

// docView — документ в том виде, в каком его видит браузер.
// Content отдаётся только там, где он реально нужен (просмотр и правка):
// список документов не должен весить мегабайты.
type docView struct {
	Owner               string    `json:"owner"`
	Path                string    `json:"path"`
	Title               string    `json:"title"`
	Content             string    `json:"content,omitempty"`
	Visibility          string    `json:"visibility"`
	CommentsOn          bool      `json:"comments_on"`
	CommentsRequireAuth bool      `json:"comments_require_auth"`
	CanEdit             bool      `json:"can_edit"`
	URL                 string    `json:"url"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (s *Server) viewDoc(d *models.Doc, ownerName string, viewer *models.User, withContent bool) docView {
	v := docView{
		Owner:               ownerName,
		Path:                d.Path,
		Title:               d.Title,
		Visibility:          d.Visibility,
		CommentsOn:          d.CommentsOn,
		CommentsRequireAuth: d.CommentsRequireAuth,
		CanEdit:             viewer != nil && viewer.ID == d.OwnerID,
		URL:                 s.cfg.BaseURL + "/" + ownerName + "/" + d.Path,
		CreatedAt:           d.CreatedAt,
		UpdatedAt:           d.UpdatedAt,
	}
	if withContent {
		v.Content = d.Content
	}
	return v
}

func (s *Server) findUser(username string) (*models.User, bool) {
	var u models.User
	err := s.db.Where("username = ?", mdpath.CanonicalUsername(username)).First(&u).Error
	return &u, err == nil
}

// docForRequest достаёт документ по адресу из URL и проверяет право на
// просмотр. Отдельно возвращает владельца: он нужен и для подписи, и для
// проверки прав. Приватный документ отвечает 404 — существование чужих
// закрытых записей не подтверждаем.
func (s *Server) docForRequest(w http.ResponseWriter, r *http.Request, viewer *models.User) (*models.Doc, *models.User, bool) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return nil, nil, false
	}
	path, err := mdpath.Normalize(r.PathValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil, nil, false
	}
	var doc models.Doc
	err = s.db.Where("owner_id = ? AND path = ?", owner.ID, path).First(&doc).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return nil, nil, false
	}
	if errors.Is(err, gorm.ErrRecordNotFound) ||
		(!doc.IsPublic() && (viewer == nil || viewer.ID != owner.ID)) {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return nil, nil, false
	}
	return &doc, owner, true
}

// ---------------------------------------------------------------- чтение

// listMine — все свои документы, включая закрытые.
func (s *Server) listMine(w http.ResponseWriter, r *http.Request, u *models.User) {
	s.respondDocList(w, u, u)
}

// listByOwner — публичный список документов пользователя.
func (s *Server) listByOwner(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	s.respondDocList(w, owner, s.authenticate(r))
}

func (s *Server) respondDocList(w http.ResponseWriter, owner, viewer *models.User) {
	q := s.db.Where("owner_id = ?", owner.ID)
	if viewer == nil || viewer.ID != owner.ID {
		q = q.Where("visibility = ?", models.VisPublic)
	}
	var docs []models.Doc
	if err := q.Order("path").Find(&docs).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	out := make([]docView, 0, len(docs))
	for i := range docs {
		out = append(out, s.viewDoc(&docs[i], owner.Username, viewer, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"owner": owner.Username, "docs": out})
}

func (s *Server) getDoc(w http.ResponseWriter, r *http.Request) {
	viewer := s.authenticate(r)
	doc, owner, ok := s.docForRequest(w, r, viewer)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.viewDoc(doc, owner.Username, viewer, true))
}

// ---------------------------------------------------------------- запись

func (s *Server) putDoc(w http.ResponseWriter, r *http.Request, u *models.User) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	if owner.ID != u.ID {
		writeErr(w, http.StatusForbidden, "править можно только свои документы")
		return
	}
	path, err := mdpath.Normalize(r.PathValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var in struct {
		Title               *string `json:"title"`
		Content             *string `json:"content"`
		Visibility          *string `json:"visibility"`
		CommentsOn          *bool   `json:"comments_on"`
		CommentsRequireAuth *bool   `json:"comments_require_auth"`
	}
	if !decodeJSON(w, r, &in, int64(s.cfg.MaxDocBytes)+8192) {
		return
	}
	if in.Content != nil && len(*in.Content) > s.cfg.MaxDocBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "документ больше допустимого размера")
		return
	}
	if in.Visibility != nil && *in.Visibility != models.VisPublic && *in.Visibility != models.VisPrivate {
		writeErr(w, http.StatusBadRequest, "видимость бывает только public или private")
		return
	}
	if in.Title != nil && len([]rune(*in.Title)) > 255 {
		writeErr(w, http.StatusBadRequest, "заголовок длиннее 255 символов")
		return
	}

	var doc models.Doc
	err = s.db.Where("owner_id = ? AND path = ?", owner.ID, path).First(&doc).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		doc = models.Doc{
			OwnerID:    owner.ID,
			Path:       path,
			Title:      defaultTitle(path),
			Visibility: models.VisPrivate, // по умолчанию всё закрыто
			CommentsOn: true,
		}
		if in.Content != nil {
			doc.Content = *in.Content
		}
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	default:
		if in.Content != nil {
			doc.Content = *in.Content
		}
	}

	if in.Title != nil {
		doc.Title = strings.TrimSpace(*in.Title)
	}
	if in.Visibility != nil {
		doc.Visibility = *in.Visibility
	}
	if in.CommentsOn != nil {
		doc.CommentsOn = *in.CommentsOn
	}
	if in.CommentsRequireAuth != nil {
		doc.CommentsRequireAuth = *in.CommentsRequireAuth
	}

	if err := s.db.Save(&doc).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "не смог сохранить документ")
		return
	}
	writeJSON(w, http.StatusOK, s.viewDoc(&doc, owner.Username, u, true))
}

func (s *Server) deleteDoc(w http.ResponseWriter, r *http.Request, u *models.User) {
	owner, ok := s.findUser(r.PathValue("owner"))
	if !ok {
		writeErr(w, http.StatusNotFound, "нет такого пользователя")
		return
	}
	if owner.ID != u.ID {
		writeErr(w, http.StatusForbidden, "удалять можно только свои документы")
		return
	}
	path, err := mdpath.Normalize(r.PathValue("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res := s.db.Where("owner_id = ? AND path = ?", owner.ID, path).Delete(&models.Doc{})
	if res.Error != nil {
		writeErr(w, http.StatusInternalServerError, "не смог удалить документ")
		return
	}
	if res.RowsAffected == 0 {
		writeErr(w, http.StatusNotFound, "нет такого документа")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// defaultTitle — заголовок из имени файла, если своего не дали.
func defaultTitle(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	return strings.TrimSuffix(path, ".md")
}
