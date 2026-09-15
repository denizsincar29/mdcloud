package api_test

import (
	"net/http"
	"testing"
)

func usersOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["users"].([]any)
	if !ok {
		t.Fatalf("в ответе нет users: %v", body)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	return out
}

// TestUserSuggestions — подсказка к полю «кому отправить»: по началу
// юзернейма облако говорит, кто есть. Только имена, только вошедшим и без
// самого себя.
func TestUserSuggestions(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	register(t, srv, "vasilisa", "parol1234")
	register(t, srv, "vasya", "parol1234")
	register(t, srv, "kostya", "parol1234")

	st, body := req(t, srv, "GET", "/api/users?q=va", deniz, nil)
	if st != http.StatusOK {
		t.Fatalf("подсказка: %d %v", st, body)
	}
	if got := usersOf(t, body); len(got) != 2 || got[0] != "vasilisa" || got[1] != "vasya" {
		t.Errorf("подсказка по «va»: %v", got)
	}

	// Регистр не важен: юзернейм хранится в нижнем регистре.
	if _, body = req(t, srv, "GET", "/api/users?q=VASIL", deniz, nil); len(usersOf(t, body)) != 1 {
		t.Errorf("подсказка по «VASIL»: %v", body)
	}
	// Себя в подсказке нет — отправить документ себе нельзя.
	if _, body = req(t, srv, "GET", "/api/users?q=den", deniz, nil); len(usersOf(t, body)) != 0 {
		t.Errorf("в подсказке оказался сам: %v", body)
	}
	// Пустой запрос — не «покажи всех»: список аккаунтов целиком не отдаём.
	if _, body = req(t, srv, "GET", "/api/users", deniz, nil); len(usersOf(t, body)) != 0 {
		t.Errorf("пустой запрос отдал людей: %v", body)
	}
	// И анониму — ничего.
	if st, _ = req(t, srv, "GET", "/api/users?q=va", "", nil); st != http.StatusUnauthorized {
		t.Errorf("подсказка без входа: %d, ждали 401", st)
	}
}

// TestUserSuggestionsEscape — «_» и «%» в юзернейме это символы, а не шаблон:
// иначе поиск по «a_» находил бы любого с одной буквой после «a».
func TestUserSuggestionsEscape(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	register(t, srv, "a_b", "parol1234")
	register(t, srv, "axb", "parol1234")

	_, body := req(t, srv, "GET", "/api/users?q=a_", deniz, nil)
	got := usersOf(t, body)
	if len(got) != 1 || got[0] != "a_b" {
		t.Errorf("подсказка по «a_»: %v", got)
	}
	if _, body = req(t, srv, "GET", "/api/users?q=%25", deniz, nil); len(usersOf(t, body)) != 0 {
		t.Errorf("«%%» нашёл людей: %v", body)
	}
}
