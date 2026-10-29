#!/bin/sh
# Подставной claude для тестов: записывает аргументы и stdin, печатает
# заготовленный ответ.
for a in "$@"; do printf '[%s]\n' "$a"; done > "$FAKE_DIR/args"
cat > "$FAKE_DIR/stdin"
pwd > "$FAKE_DIR/pwd"
[ -n "$FAKE_SLEEP" ] && sleep "$FAKE_SLEEP"
cat "$FAKE_DIR/out"
exit "${FAKE_EXIT:-0}"
