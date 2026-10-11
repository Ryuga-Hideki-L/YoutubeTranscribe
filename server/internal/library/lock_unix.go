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
// друга целиком, каждый из своей памяти. Блокировку снимает release. Она
// снимается и сама при выходе процесса, потому что flock держится открытым
// файлом.
func LockDir(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
		}
		return nil, fmt.Errorf("блокировка %s: %w", dir, err)
	}
	return func() { file.Close() }, nil
}
