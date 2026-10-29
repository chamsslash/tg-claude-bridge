# Три образа из одного файла (compose выбирает target):
#   bridge — сам мост;
#   claude — Claude Code CLI и обёртка claude-runner, без секретов моста;
#   egress — единственный выход контейнера claude наружу.

# alpine, а не полный golang: тот тянет больше гигабайта, а бинари всё равно
# статические (CGO_ENABLED=0) и от базового образа ничего не наследуют.
FROM golang:1.27-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN for c in tg-bridge egress claude-runner; do \
      CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/$c ./cmd/$c || exit 1; \
    done

# Каталог состояния создаётся здесь: в distroless нет шелла, mkdir там не
# сделать, а именованный том при первом монтировании наследует владельца
# каталога из образа — так nonroot сможет в него писать.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static:nonroot AS egress
COPY --from=build /out/egress /egress
EXPOSE 3128
USER nonroot:nonroot
ENTRYPOINT ["/egress"]

FROM node:22.12.0-bookworm-slim AS claude
# Версия CLI закреплена: обновление — осознанная пересборка, а не сюрприз
# посреди ночи. Автообновление внутри контейнера выключено ниже.
ARG CLAUDE_CODE_VERSION=2.1.308
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION} && npm cache clean --force
COPY --from=build /out/claude-runner /usr/local/bin/claude-runner
# Никакого лишнего трафика: наружу пускает только egress, и попытки
# телеметрии и обновлений были бы шумом в его логе.
ENV DISABLE_AUTOUPDATER=1 \
    DISABLE_TELEMETRY=1 \
    DISABLE_ERROR_REPORTING=1 \
    CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
    ENABLE_CLAUDEAI_MCP_SERVERS=false \
    CLAUDE_CODE_DISABLE_ARTIFACT=1 \
    RUNNER_ADDR=:8090
USER node
WORKDIR /home/node
EXPOSE 8090
ENTRYPOINT ["claude-runner"]

# Последним — чтобы `docker build .` без target собирал мост.
FROM gcr.io/distroless/static:nonroot AS bridge

COPY --from=build /out/tg-bridge /tg-bridge
COPY --from=build --chown=nonroot:nonroot /out/data /data

ENV STATE_PATH=/data/state.json

USER nonroot:nonroot
ENTRYPOINT ["/tg-bridge"]
