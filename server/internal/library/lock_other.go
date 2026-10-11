//go:build !unix

package library

import "errors"

// ErrLocked — папку данных держит другой запущенный сервер.
var ErrLocked = errors.New("папку данных уже занимает другой запущенный «Дословно»")

// LockDir ничего не блокирует, потому что здесь нет flock. Второй сервер не обнаруживается.
func LockDir(string) (func(), error) { return func() {}, nil }
