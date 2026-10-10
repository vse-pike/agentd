// Package schedule — расписание повторяющихся задач: разбор заголовка
// из текста задачи и вычисление следующего момента запуска.
package schedule

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const header = "ПОВТОРЯЮЩАЯСЯ ЗАДАЧА"

var timeRe = regexp.MustCompile(`([01]?\d|2[0-3]):([0-5]\d)`)

// Parse выделяет из текста задачи заголовок повторения и тело.
// Заголовок — первая строка вида «ПОВТОРЯЮЩАЯСЯ ЗАДАЧА КАЖДЫЙ ДЕНЬ - 09:00».
// Нет заголовка — задача разовая: spec = "", тело равно исходному тексту.
func Parse(text string) (spec, body string, err error) {
	line, rest, hasMore := strings.Cut(strings.TrimSpace(text), "\n")
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(line)), header) {
		return "", text, nil
	}
	if !hasMore {
		return "", "", fmt.Errorf("после заголовка «%s» нет тела задачи", header)
	}

	head := strings.ToUpper(strings.TrimSpace(line))
	switch {
	case strings.Contains(head, "КАЖДЫЙ ДЕНЬ") || strings.Contains(head, "ЕЖЕДНЕВНО"):
		m := timeRe.FindStringSubmatch(line)
		if m == nil {
			return "", "", fmt.Errorf("в заголовке нет времени; формат: «%s КАЖДЫЙ ДЕНЬ - 09:00»", header)
		}
		h := m[1]
		if len(h) == 1 {
			h = "0" + h
		}
		spec = "daily " + h + ":" + m[2]
	default:
		return "", "", fmt.Errorf("не понял расписание; пока умею только «%s КАЖДЫЙ ДЕНЬ - 09:00»", header)
	}

	if body = strings.TrimSpace(rest); body == "" {
		return "", "", fmt.Errorf("после заголовка «%s» нет тела задачи", header)
	}
	return spec, body, nil
}

// Next — ближайший момент запуска для spec строго позже from, в зоне from.
func Next(spec string, from time.Time) (time.Time, error) {
	kind, hhmm, ok := strings.Cut(spec, " ")
	if !ok {
		return time.Time{}, fmt.Errorf("неизвестное расписание %q", spec)
	}
	var h, m int
	_, scanErr := fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	if kind != "daily" || scanErr != nil || h > 23 || m > 59 {
		return time.Time{}, fmt.Errorf("неизвестное расписание %q", spec)
	}
	next := time.Date(from.Year(), from.Month(), from.Day(), h, m, 0, 0, from.Location())
	if !next.After(from) {
		next = next.AddDate(0, 0, 1)
	}
	return next, nil
}

// Describe — расписание по-человечески, для сообщений и /list.
func Describe(spec string) string {
	if _, hhmm, ok := strings.Cut(spec, " "); ok {
		return "ежедневно в " + hhmm
	}
	return spec
}
