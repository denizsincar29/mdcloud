package api

import (
	"net/http"
	"strings"

	"github.com/denizsincar29/mdcloud/internal/mdpath"
	"github.com/denizsincar29/mdcloud/internal/models"
)

// Поиск людей по юзернейму — подсказка к полю «кому отправить». Набирать
// юзернейм по памяти неудобно и легко промахнуться, а облако и так знает, кто
// в нём заведён.
//
// Отдаём только имена и только вошедшим: список аккаунтов — не публичные
// данные, а почта и прочее чужое сюда не попадает вовсе. Себя из подсказки
// убираем — отправить документ самому себе нельзя, и предлагать это незачем.

// usersLimit — сколько подсказок отдаём. Поле текстовое, а не выпадающий
// список: человек набирает имя, подсказка лишь помогает не ошибиться, и
// десятка хватает, чтобы попасть в нужного.
const usersLimit = 10

// listUsers — GET /api/users?q=<начало юзернейма>: кто в облаке есть.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request, u *models.User) {
	q := mdpath.CanonicalUsername(r.URL.Query().Get("q"))
	var users []models.User
	tx := s.db.Model(&models.User{}).Where("id <> ?", u.ID)
	// Пустой запрос — не «покажи всех», а «пока ничего не набрано»: иначе
	// подсказка открывала бы список аккаунтов целиком.
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"users": []string{}})
		return
	}
	// Подчёркивание и процент в юзернейме — обычные символы, а не шаблон:
	// экранируем, иначе «_» совпадало бы с любой буквой.
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	if err := tx.Where(`username LIKE ? ESCAPE '\'`, esc+"%").
		Order("username").Limit(usersLimit).
		Find(&users).Error; err != nil {
		writeErr(w, http.StatusInternalServerError, "база недоступна")
		return
	}
	names := make([]string, 0, len(users))
	for i := range users {
		names = append(names, users[i].Username)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": names})
}
