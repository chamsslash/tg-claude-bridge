package telegram

import "strings"

// Split режет текст на куски не длиннее limit, стараясь рвать по границам
// строк. Счёт идёт в символах, а не в байтах: лимит Telegram символьный, и
// байтовая резка вдобавок разрубила бы кириллицу посреди руны.
//
// Строку длиннее лимита приходится рвать жёстко — иначе кусок не уедет вовсе.
func Split(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	if count(text) <= limit {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}

	var parts []string
	var cur strings.Builder
	curLen := 0

	flush := func() {
		if cur.Len() > 0 {
			parts = append(parts, cur.String())
			cur.Reset()
			curLen = 0
		}
	}

	for _, line := range splitAfterNewline(text) {
		for count(line) > limit {
			flush()
			head, tail := cutRunes(line, limit)
			parts = append(parts, head)
			line = tail
		}
		if curLen+count(line) > limit {
			flush()
		}
		cur.WriteString(line)
		curLen += count(line)
	}
	flush()

	// Хвост из одних пробелов Telegram отвергает как пустое сообщение.
	out := parts[:0]
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return out
}

func count(s string) int {
	return len([]rune(s))
}

// cutRunes отрезает первые n символов, не разрубая руну.
func cutRunes(s string, n int) (head, tail string) {
	r := []rune(s)
	return string(r[:n]), string(r[n:])
}

// splitAfterNewline делит текст на строки, сохраняя переводы строк: без них
// склейка кусков обратно дала бы не исходный текст.
func splitAfterNewline(s string) []string {
	var out []string
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			if s != "" {
				out = append(out, s)
			}
			return out
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
}
