#!/usr/bin/env bash
# Разворачивает мост на сервере: обновляет код, пересобирает образы,
# перезапускает контейнеры и убеждается, что бот действительно слушает.
#
# Запускать на самом сервере, из каталога репозитория:
#   ./deploy.sh              — git pull + пересборка + перезапуск
#   ./deploy.sh --no-pull    — то же, но без git pull (деплой локальных правок)
set -euo pipefail

cd "$(dirname "$0")"

PULL=1
[[ "${1:-}" == "--no-pull" ]] && PULL=0

die() { echo "ОШИБКА: $*" >&2; exit 1; }

command -v docker >/dev/null || die "docker не установлен"

# Compose бывает плагином (v2, «docker compose») и отдельным бинарём (v1,
# «docker-compose»). Берём что есть, чтобы скрипт не падал на старом сервере.
if docker compose version >/dev/null 2>&1; then
  DC=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  DC=(docker-compose)
else
  die "не найден docker compose (ни плагин v2, ни docker-compose v1)"
fi

# Старый мост жил в systemd. Два читателя одного бота отбирают друг у друга
# очередь (409 Conflict), поэтому со старым юнитом не стартуем.
if command -v systemctl >/dev/null && systemctl is-active --quiet tg-bridge 2>/dev/null; then
  die "работает старый systemd-юнит tg-bridge — останови его: sudo systemctl disable --now tg-bridge"
fi

# --- конфиг ---------------------------------------------------------------
envval() {
  local line
  line="$(grep -E "^$1=" "${2:-.env}" | head -1 || true)"
  printf '%s' "${line#*=}"
}

# Первый прогон создаёт .env из шаблона и останавливается на проверке ниже:
# секретов в шаблоне нет, их вписывает человек.
if [[ ! -f .env ]]; then
  cp .env.example .env
  chmod 600 .env
  echo "==> .env не было — создал из .env.example, заполни секреты"
fi
[[ -n "$(envval TELEGRAM_BOT_TOKEN)" ]] || die "в .env не заполнен TELEGRAM_BOT_TOKEN"

# Токен Claude — в отдельном файле: его получает только контейнер claude.
if [[ ! -f claude.env ]]; then
  cp claude.env.example claude.env
  chmod 600 claude.env
  echo "==> claude.env не было — создал из claude.env.example"
fi
if [[ -z "$(envval CLAUDE_CODE_OAUTH_TOKEN claude.env)" ]]; then
  echo "==> ВНИМАНИЕ: в claude.env пуст CLAUDE_CODE_OAUTH_TOKEN — сводки, команды"
  echo "    календаря и еда по фото работать не будут. Токен: claude setup-token"
fi
[[ -n "$(envval GOOGLE_REFRESH_TOKEN)" ]] || echo "==> GOOGLE_* в .env не заданы — Gmail и Календарь выключены"
[[ -n "$(envval YAZIO_USERNAME)" ]] || echo "==> YAZIO_* в .env не заданы — еда по фото выключена"

# --- код ------------------------------------------------------------------
if [[ $PULL -eq 1 ]]; then
  if [[ -d .git ]]; then
    echo "==> git pull"
    git pull --ff-only
  else
    echo "==> не git-репозиторий, шаг обновления пропущен"
  fi
fi

# --- сборка и запуск ------------------------------------------------------
echo "==> сборка образов и перезапуск"
since="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
if ! "${DC[@]}" up -d --build --remove-orphans; then
  echo >&2
  echo "Сборка или запуск не удались. Если выше есть «no space left on device» —" >&2
  echo "это кончился диск: docker system prune -af, затем ./deploy.sh снова." >&2
  exit 1
fi

# --- проверка -------------------------------------------------------------
# «compose up прошёл» и «бот слушает» — разные события: ждём строку из лога.
# Контейнер без изменений compose не пересоздаёт — тогда хватает того, что
# он работает и не перезапускается.
echo -n "==> ждём, пока бот начнёт слушать "
ok=""
for _ in $(seq 1 30); do
  if "${DC[@]}" logs --since "$since" bridge 2>/dev/null | grep -q 'слушаю бота'; then
    ok=1
    break
  fi
  cid="$("${DC[@]}" ps -q bridge 2>/dev/null || true)"
  if [[ -n "$cid" ]] && [[ "$(docker inspect -f '{{.State.Running}} {{.RestartCount}}' "$cid" 2>/dev/null)" == "true 0" ]] \
     && [[ "$(docker inspect -f '{{.State.StartedAt}}' "$cid")" < "$since" ]]; then
    ok=1
    break
  fi
  echo -n "."
  sleep 2
done
echo
if [[ -z "$ok" ]]; then
  echo "БОТ НЕ ЗАПУСТИЛСЯ за 60 секунд. Последние логи:" >&2
  "${DC[@]}" logs --tail=50 bridge >&2
  exit 1
fi
echo "==> бот слушает"

# Обёртка отвечает изнутри своей сети; сам Claude проверится на первом
# запросе (о долгом сбое бот напишет сам).
if "${DC[@]}" exec -T claude node -e 'fetch("http://127.0.0.1:8090/healthz").then(r=>process.exit(r.ok?0:1),()=>process.exit(1))' 2>/dev/null; then
  echo "==> обёртка Claude отвечает"
else
  echo "==> обёртка Claude НЕ отвечает: ${DC[*]} logs claude"
fi
