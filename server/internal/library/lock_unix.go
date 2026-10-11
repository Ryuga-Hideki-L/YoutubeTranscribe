//go:build unix

package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLocked — папку данных держит другой запущенный сервер.
var ErrLocked = errors.New("папку данных уже занимает другой запущенный «Дословно»")

// LockDir берёт исключительную блокировку папки данных на всё время жизни
// процесса. Два сервера на одной папке переписывали бы файлы каналов друг
// друга целиком, каждый из своей памяти. Блокировка снимается release или
// сама при выходе процесса (flock держится открытым файлом).
func LockDir(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
		}
		return nil, fmt.Errorf("блокировка %s: %w", dir, err)
	}
	return func() { f.Close() }, nil
}
