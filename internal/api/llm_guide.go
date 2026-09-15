package api

import (
	_ "embed"
	"net/http"
)

// Инструкция для ассистентов: как пользоваться этим API. Лежит рядом с
// обработчиками и едет в бинарнике — так она не отстаёт от кода: поменялся
// эндпоинт, здесь же поправили текст.
//
//go:embed llm.md
var llmGuide string

// llmGuideHandler отдаёт её по адресу /api/llm.md.
//
// Файл публичный: он не содержит ни секретов, ни чужих данных, а прочитать его
// должен уметь и тот, у кого ключа ещё нет — иначе непонятно, как его выписать.
func (s *Server) llmGuideHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	// Короткий кеш у клиента: инструкция меняется редко, но и вчерашняя копия
	// не должна пережить правку API.
	w.Header().Set("Cache-Control", "public, max-age=300")
	if _, err := w.Write([]byte(llmGuide)); err != nil {
		return
	}
}
