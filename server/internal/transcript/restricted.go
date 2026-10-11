package transcript

import (
	"context"
	"net"
	"slices"
	"strings"
)

// RestrictedMode проверяет, не подменяет ли DNS сети www.youtube.com на
// restrict.youtube.com или restrictmoderate.youtube.com.
// Так включают «Безопасный режим» YouTube публичные Wi-Fi и родительские фильтры.
// Субтитры обычных видео он не прячет.
// Но часть видео становится недоступной, и индексация помечает их unavailable.
func RestrictedMode(ctx context.Context, resolver *net.Resolver) bool {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	if cname, err := resolver.LookupCNAME(ctx, "www.youtube.com"); err == nil && strings.HasPrefix(cname, "restrict") {
		return true
	}
	// CNAME бывает скрыт, когда DNS подменяет сразу адрес. Поэтому сверяем адреса.
	wwwAddrs, err := resolver.LookupHost(ctx, "www.youtube.com")
	if err != nil {
		return false
	}
	for _, host := range []string{"restrict.youtube.com", "restrictmoderate.youtube.com"} {
		hostAddrs, err := resolver.LookupHost(ctx, host)
		if err != nil {
			continue
		}
		for _, addr := range hostAddrs {
			if slices.Contains(wwwAddrs, addr) {
				return true
			}
		}
	}
	return false
}
