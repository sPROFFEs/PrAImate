package serverboot

import (
	"os"
	"sync"
)

// Keep startup and connector diagnostics bounded even when the VM stays online
// for weeks. Child output flows through this writer rather than a retained FD.
type rotatingLog struct {
	mu    sync.Mutex
	file  *os.File
	path  string
	limit int64
}

func (l *rotatingLog) Write(body []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return 0, os.ErrClosed
	}
	original := len(body)
	if int64(len(body)) > l.limit {
		body = body[int64(len(body))-l.limit:]
	}
	info, err := l.file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() > 0 && info.Size()+int64(len(body)) > l.limit {
		if err = l.file.Close(); err != nil {
			return 0, err
		}
		l.file = nil
		if err = os.Remove(l.path + ".previous"); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		if err = os.Rename(l.path, l.path+".previous"); err != nil {
			return 0, err
		}
		l.file, err = os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return 0, err
		}
	}
	_, err = l.file.Write(body)
	if err != nil {
		return 0, err
	}
	return original, nil
}
func (l *rotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
