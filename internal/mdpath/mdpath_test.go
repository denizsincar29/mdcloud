package mdpath

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
		bad  bool
	}{
		{in: "ДЗ по ИИ", bad: true}, // пробел в имени — %20 в адресе
		{in: "ДЗ-по-ИИ/задачи", want: "ДЗ-по-ИИ/задачи"},
		{in: "  /ДЗ/ИИ/задачи.md  ", want: "ДЗ/ИИ/задачи.md"},
		{in: "a//b", want: "a/b"},    // пустые сегменты выбрасываем
		{in: "./a/./b", want: "a/b"}, // точки-заполнители тоже
		{in: "../../etc/passwd", bad: true},
		{in: "a/../b", bad: true},
		{in: "", bad: true},
		{in: "/", bad: true},
		{in: "a/b/c/d/e/f", bad: true}, // глубже пяти папок не пускаем
		{in: "a/b/c/d/e", want: "a/b/c/d/e"},
		{in: ".hidden", bad: true},
		{in: "имя+файл(1)", want: "имя+файл(1)"},
	}
	for _, c := range cases {
		got, err := Normalize(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("Normalize(%q) = %q, ожидалась ошибка", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Normalize(%q): неожиданная ошибка %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Normalize(%q) = %q, хотели %q", c.in, got, c.want)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ДЗ/ИИ/задачи", "dz/ii/zadachi"},
		{"витрина/облако-и-редактор", "vitrina/oblako-i-redaktor"},
		{"ДЗ-по-ИИ/задачи.md", "dz-po-ii/zadachi.md"},
		{"уже/латиница", "uzhe/latinitsa"},
		{"readme.md", "readme.md"},        // латиница едет как есть
		{"ReadMe.md", "readme.md"},        // но регистр теряется
		{"объём ёлки", "obem elki"},       // ъ пропадает, ё — как «е»
		{"ЖЮЛЬ/ЩАВЕЛЬ", "zhyul/shchavel"}, // ж, ю, щ
		{"письмо(1)+черновик", "pismo(1)+chernovik"},
		{"ь/ъ", "ь/ъ"},                  // из одних «ь»/«ъ» транслита не выйдет
		{"греческий/α", "grecheskiy/α"}, // чужой буквы в таблице нет — сегмент как есть
	}
	for _, c := range cases {
		if got := Slug(c.in); got != c.want {
			t.Errorf("Slug(%q) = %q, хотели %q", c.in, got, c.want)
		}
	}
}

func TestValidUsername(t *testing.T) {
	for _, ok := range []string{"deniz", "va_sya-1", "abc"} {
		if !ValidUsername(ok) {
			t.Errorf("%q должен быть допустим", ok)
		}
	}
	for _, bad := range []string{"", "ab", "Дениз", "Deniz", "de niz", "deniz!", "оченьдлинноеимякоторогонебываеттакого"} {
		if ValidUsername(bad) {
			t.Errorf("%q должен быть отвергнут", bad)
		}
	}
}
