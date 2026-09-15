// Package mdpath — правила адресов в облаке.
//
// Адрес документа — это <username>/<sub>/<folder>/<name>, и он же ключ в БД.
// Поэтому нормализация строгая: один и тот же документ не должен уметь
// притвориться двумя разными строками.
package mdpath

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// MaxSegments — сколько сегментов допускаем после имени владельца.
const MaxSegments = 5

// MaxLen — предел длины адреса целиком.
const MaxLen = 200

// Ошибки валидации — на русском: их видит человек в интерфейсе.
var (
	ErrEmpty     = errors.New("пустой адрес документа")
	ErrTooDeep   = fmt.Errorf("слишком глубоко: не больше %d вложенных папок", MaxSegments)
	ErrTooLong   = fmt.Errorf("адрес длиннее %d символов", MaxLen)
	ErrTraversal = errors.New("сегмент «..» запрещён")
	ErrBadSeg    = errors.New("в имени папки или файла допустимы буквы, цифры, точка, дефис и подчёркивание")
)

// Normalize приводит адрес к каноническому виду.
//
//	"  /ДЗ/ИИ/Задачи.md " -> "ДЗ/ИИ/Задачи.md"
//
// Возвращает ошибку, если адрес невалиден: пустые сегменты выбрасываются,
// «..» и точки-заполнители отбрасываются с ошибкой, пробелы в сегменте
// запрещены (пробел в URL — это %20, такое имя потом не набрать руками).
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmpty
	}
	var segs []string
	for _, part := range strings.Split(raw, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return "", ErrTraversal
		}
		if !validSegment(part) {
			return "", fmt.Errorf("%w: «%s»", ErrBadSeg, part)
		}
		segs = append(segs, part)
	}
	if len(segs) == 0 {
		return "", ErrEmpty
	}
	if len(segs) > MaxSegments {
		return "", ErrTooDeep
	}
	out := strings.Join(segs, "/")
	if len([]rune(out)) > MaxLen {
		return "", ErrTooLong
	}
	return out, nil
}

// validSegment проверяет один сегмент адреса.
func validSegment(s string) bool {
	if s == "" || len([]rune(s)) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '-', r == '_', r == '.', r == '+', r == '(', r == ')':
		default:
			return false
		}
	}
	// Сегмент не может начинаться с точки: «.» и «..» — служебные имена,
	// а скрытые файлы в облаке ни к чему.
	return !strings.HasPrefix(s, ".")
}

// ValidUsername проверяет имя владельца: 3–32 символа, латиница в нижнем
// регистре, цифры, дефис и подчёркивание. Имя попадает в URL, поэтому
// кириллицу здесь не пускаем — она уходит в путь документа.
func ValidUsername(s string) bool {
	if len(s) < 3 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// CanonicalUsername приводит имя к нижнему регистру.
func CanonicalUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
