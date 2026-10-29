// Package msk — московское время. Сервер и Claude живут в UTC, а читает
// сообщения человек в Москве.
package msk

import "time"

// Location грузится один раз. В урезанных образах базы часовых поясов может
// не оказаться — тогда фиксированный +03:00: Москва живёт без перехода на
// летнее время с 2014 года, смещение постоянное.
var Location = load()

func load() *time.Location {
	if loc, err := time.LoadLocation("Europe/Moscow"); err == nil {
		return loc
	}
	return time.FixedZone("MSK", 3*60*60)
}
