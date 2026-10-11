package transcript

import (
	"context"
	"net"
	"slices"
	"strings"
)

// RestrictedMode проверяет, не подменяет ли DNS сети www.youtube.com на
// restrict.youtube.com или restrictmoderate.youtube.com. Так включают
// «Безопасный режим» YouTube публичные Wi-Fi и родительские фильтры.
// Субтитры обычных видео он не прячет, но часть видео становится
// недоступной, и индексация помечает их unavailable.
func RestrictedMode(ctx context.Context, r *net.Resolver) bool {
	if r == nil {
		r = net.DefaultResolver
	}
	if cname, err := r.LookupCNAME(ctx, "www.youtube.com"); err == nil && strings.HasPrefix(cname, "restrict") {
		return true
	}
	// CNAME бывает скрыт (подмена прямо адресом) — сверяем адреса.
	www, err := r.LookupHost(ctx, "www.youtube.com")
	if err != nil {
		return false
	}
	for _, host := range []string{"restrict.youtube.com", "restrictmoderate.youtube.com"} {
		addrs, err := r.LookupHost(ctx, host)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if slices.Contains(www, a) {
				return true
			}
		}
	}
	return false
}
