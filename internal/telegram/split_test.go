package telegram

import (
	"strings"
	"testing"
)

func TestSplitKeepsShortTextWhole(t *testing.T) {
	got := Split("привет", MessageLimit)
	if len(got) != 1 || got[0] != "привет" {
		t.Fatalf("короткий текст не должен резаться, получено %q", got)
	}
}

func TestSplitCountsRunesNotBytes(t *testing.T) {
	// Десять кириллических букв — это двадцать байт. Резка по байтам сочла бы
	// текст превысившим лимит и разрубила бы руну пополам.
	got := Split(strings.Repeat("я", 10), 10)
	if len(got) != 1 {
		t.Fatalf("ожидал один кусок, получено %d: %q", len(got), got)
	}
}

func TestSplitBreaksOnLineBoundaries(t *testing.T) {
	text := "первая строка\nвторая строка\nтретья строка\n"
	got := Split(text, 20)

	for _, p := range got {
		if len([]rune(p)) > 20 {
			t.Fatalf("кусок длиннее лимита: %q", p)
		}
	}
	if strings.Join(got, "") != text {
		t.Fatalf("склейка не совпала с оригиналом:\n%q\n%q", strings.Join(got, ""), text)
	}
}

func TestSplitHardCutsOverlongLine(t *testing.T) {
	line := strings.Repeat("а", 250)
	got := Split(line, 100)

	if len(got) != 3 {
		t.Fatalf("ожидал три куска, получено %d", len(got))
	}
	if strings.Join(got, "") != line {
		t.Fatal("жёсткая резка потеряла или переставила текст")
	}
	for _, p := range got {
		if len([]rune(p)) > 100 {
			t.Fatalf("кусок длиннее лимита: %d символов", len([]rune(p)))
		}
	}
}

func TestSplitDropsBlankParts(t *testing.T) {
	// Telegram отвергает сообщение из одних пробелов, поэтому такие куски
	// до отправки доходить не должны.
	if got := Split("   \n\n  ", 10); len(got) != 0 {
		t.Fatalf("ожидал пустой результат, получено %q", got)
	}
}
