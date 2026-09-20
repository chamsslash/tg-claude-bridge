#!/usr/bin/env python3
"""Ответ в Telegram одной командой — для облачной рутины.

    python3 scripts/say.py "короткий ответ"
    python3 scripts/say.py < ответ.txt

Нужен, чтобы рутине не приходилось собирать JSON внутри shell-строки: кавычки
и переводы строк в тексте ответа ломают такую сборку тихо и по-разному.

Берёт TELEGRAM_BOT_TOKEN и TELEGRAM_CHAT_ID из окружения. Только стандартная
библиотека — в окружении рутины ничего не устанавливается.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

LIMIT = 4096


def split_text(text, limit=LIMIT):
    """Режем по границам строк; строку длиннее лимита — жёстко.

    Счёт в символах, а не в байтах: лимит Telegram символьный, и байтовая
    резка вдобавок разрубила бы кириллицу посреди буквы.
    """
    parts, cur = [], ""
    for line in text.splitlines(keepends=True):
        while len(line) > limit:
            if cur:
                parts.append(cur)
                cur = ""
            parts.append(line[:limit])
            line = line[limit:]
        if len(cur) + len(line) > limit:
            parts.append(cur)
            cur = ""
        cur += line
    if cur.strip():
        parts.append(cur)
    return parts


def send(text):
    url = (f"https://api.telegram.org/bot{os.environ['TELEGRAM_BOT_TOKEN']}"
           f"/sendMessage")
    payload = json.dumps({"chat_id": int(os.environ["TELEGRAM_CHAT_ID"]),
                          "text": text,
                          "disable_web_page_preview": True}).encode()
    req = urllib.request.Request(url, data=payload,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            body = json.loads(r.read())
    except urllib.error.HTTPError as e:
        body = json.loads(e.read() or b"{}")
    except (urllib.error.URLError, TimeoutError) as e:
        sys.exit(f"telegram недоступен: {e}")
    if not body.get("ok"):
        sys.exit(f"telegram: {body.get('description', body)}")


def main():
    missing = [k for k in ("TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID")
               if not os.environ.get(k)]
    if missing:
        sys.exit(f"нет переменных окружения: {', '.join(missing)}")

    text = " ".join(sys.argv[1:]).strip() or sys.stdin.read().strip()
    if not text:
        sys.exit("нечего отправлять")

    for i, part in enumerate(split_text(text)):
        if i:
            time.sleep(1)
        send(part)
    print("отправлено")


if __name__ == "__main__":
    main()
