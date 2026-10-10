package schedule

import (
	"strings"
	"testing"
	"time"
)

func TestParseDaily(t *testing.T) {
	body := "Найти объявления.\n\nЧТО ИЩУ\n- кронштейн"
	for _, head := range []string{
		"ПОВТОРЯЮЩАЯСЯ ЗАДАЧА КАЖДЫЙ ДЕНЬ - 09:00",
		"повторяющаяся задача каждый день 9:00",
		"Повторяющаяся задача ЕЖЕДНЕВНО - 21:30",
	} {
		spec, got, err := Parse(head + "\n" + body)
		if err != nil {
			t.Fatalf("%q: %v", head, err)
		}
		if spec != "daily 09:00" && spec != "daily 21:30" {
			t.Fatalf("spec = %q", spec)
		}
		if got != body {
			t.Fatalf("body = %q", got)
		}
	}
}

func TestParseOneOff(t *testing.T) {
	text := "Напиши Зарине, позови гулять"
	spec, body, err := Parse(text)
	if err != nil || spec != "" || body != text {
		t.Fatalf("spec=%q body=%q err=%v", spec, body, err)
	}
}

func TestParseErrors(t *testing.T) {
	for _, text := range []string{
		"ПОВТОРЯЮЩАЯСЯ ЗАДАЧА КАЖДЫЙ ДЕНЬ - утром",   // нет времени
		"ПОВТОРЯЮЩАЯСЯ ЗАДАЧА КАЖДУЮ НЕДЕЛЮ - 09:00", // не умеем
		"ПОВТОРЯЮЩАЯСЯ ЗАДАЧА КАЖДЫЙ ДЕНЬ - 09:00",   // нет тела
	} {
		if _, _, err := Parse(text); err == nil {
			t.Fatalf("%q: ждали ошибку", text)
		}
	}
}

func TestNextDaily(t *testing.T) {
	// фиксированная зона, чтобы тест не зависел от машины
	loc := time.FixedZone("TEST", 5*3600)
	from := time.Date(2026, 10, 10, 8, 0, 0, 0, loc)

	next, err := Next("daily 09:00", from)
	if err != nil || !next.Equal(time.Date(2026, 10, 10, 9, 0, 0, 0, loc)) {
		t.Fatalf("до окна: %v %v", next, err)
	}

	next, err = Next("daily 09:00", time.Date(2026, 10, 10, 9, 0, 0, 0, loc))
	if err != nil || !next.Equal(time.Date(2026, 10, 11, 9, 0, 0, 0, loc)) {
		t.Fatalf("в окно ровно: %v %v", next, err)
	}

	next, err = Next("daily 09:00", time.Date(2026, 10, 10, 23, 0, 0, 0, loc))
	if err != nil || !next.Equal(time.Date(2026, 10, 11, 9, 0, 0, 0, loc)) {
		t.Fatalf("после окна: %v %v", next, err)
	}

	if _, err := Next("weekly 09:00", from); err == nil {
		t.Fatal("неизвестный spec должен давать ошибку")
	}
}

func TestDescribe(t *testing.T) {
	if d := Describe("daily 09:00"); !strings.Contains(d, "09:00") {
		t.Fatalf("Describe = %q", d)
	}
}
