# Автоматизация форка CLIProxyAPI: отдача `context_length`, автосинк апстрима и динамический версионинг (`vX.Y.Z-p`)

Инженерная документация по форку [`Realmagnum/CLIProxyAPI`](https://github.com/Realmagnum/CLIProxyAPI), архитектуре CI/CD, механизму динамического версионирования и регламенту эксплуатации на сервере `onedash2` (сервис `cpamp`).

---

## 1. Зачем сделан форк: ключевая наработка

### Проблема оригинального CLIProxyAPI (upstream)
Официальный проект [`router-for-me/CLIProxyAPI`](https://github.com/router-for-me/CLIProxyAPI) (образ `eceasy/cli-proxy-api`) агрегирует OAuth/CLI-подписки (ChatGPT Codex, Claude Code, Gemini CLI / Antigravity, Grok Build, Kimi) и оборачивает их в стандартные OpenAI- и Anthropic-совместимые API.

Однако в эндпоинте `GET /v1/models` оригинальный код жёстко фильтровал поля ответа:
```go
// Оригинальный код в sdk/api/handlers/openai/openai_handlers.go
filteredModel := gin.H{
    "id":       modelID,
    "object":   "model",
    "created":  1677610602,
    "owned_by": ownedBy,
}
```
Все расширенные метаданные моделей — **`context_length`**, **`max_context_length`**, **`max_completion_tokens`** — отбрасывались.

### Последствия для инфраструктуры агентов
Агентские стеки (Hermes Agent, OpenCode, Claude Dev, Cline), работающие через `cpamp` (:8317), при автодискавере моделей через `/v1/models`:
1. Не получали размер контекстного окна проксируемой модели.
2. Откатывались на консервативные дефолты (например, 8k/32k токенов) либо падали при превышении неизвестного лимита.
3. Не могли динамически рассчитывать бюджет контекста при работе с большими кодовыми базами.

В апстриме были открыты соответствующие PR (PR #5123, PR #4935, PR #6194). Несмотря на одобрение мейнтейнерами ещё в августе 2026 года, PR не вливался в релизы.

### Реализованный патч кодовой базы
В форке `Realmagnum/CLIProxyAPI` в метод `OpenAIModels` (`sdk/api/handlers/openai/openai_handlers.go`) внедрена трансляция контекстных метаданных:
```go
// Surface context metadata when the underlying ModelInfo carries it.
for _, key := range []string{"context_length", "max_context_length", "max_completion_tokens"} {
    if v, exists := model[key]; exists {
        filteredModel[key] = v
    }
}
```
Патч покрыт тестом `internal/api/server_grok_models_test.go` (`TestModelsSurfacesContextLengthForAutoDiscoveringClients`).

---

## 2. Архитектура полного цикла автоматизации

```
[Upstream Репозиторий router-for-me/CLIProxyAPI]
         │ (выход нового релиза / тега v8.0.x)
         ▼
[GitHub Actions: auto-merge-upstream.yml] (каждые 6 часов или dispatch)
         │ git fetch upstream --tags && git merge upstream/main
         ▼
[Форк Realmagnum/CLIProxyAPI @ main] ──(коммит / git push)──┐
  ├── Патч context_length (openai_handlers.go)              │
  └── Регрессионные тесты                                   ▼
                                           [GitHub Actions: docker-build-patched.yml]
                                             ├── Определяет тег апстрима: git describe upstream/main
                                             ├── Формирует VERSION = "${UPSTREAM_TAG}-p" (напр. v8.0.29-p)
                                             ├── Прогоняет тесты Go
                                             ├── Компилирует бинарник с ldflags:
                                             │     -X 'main.Version=${VERSION}'
                                             │     -X 'main.Commit=${COMMIT}'
                                             │     -X 'main.BuildDate=${BUILD_DATE}'
                                             └── Пушит мульти-теги в GHCR:
                                                   ghcr.io/realmagnum/cli-proxy-api:latest
                                                   ghcr.io/realmagnum/cli-proxy-api:v8.0.29-p
                                                   ghcr.io/realmagnum/cli-proxy-api:<commit-sha>
                                                           │
                                                           ▼
                                            [Хост onedash2: /opt/docker/CLIProxyAPI]
                                              └── Скрипт update.sh (docker compose pull && recreate)
                                              └── Сервис cpamp (:8317 healthy, версия v8.0.29-p)
```

---

## 3. Решение проблемы версионирования (`vX.Y.Z-p`)

### В чём была проблема
Ранее воркфлоу сборки хардкодил версию:
```yaml
build-args:
  VERSION=v8-patched
```
В результате:
- В Web UI / CLI отображалось безликое `CLIProxyAPI Version: v8-patched`.
- Невозможно было понять, на базе какого именно релиза апстрима собран контейнер (v8.0.20? v8.0.28? v8.0.29?).
- Терялась прозрачность отслеживания багов и совместимости с апстрим-документацией.

### Реализованное решение
В `.github/workflows/docker-build-patched.yml` добавлен динамический расчет версии:
```yaml
      - name: Generate build metadata
        id: meta
        run: |
          git remote add upstream https://github.com/router-for-me/CLIProxyAPI.git || true
          git fetch upstream --tags || true
          UPSTREAM_TAG=$(git describe --tags --abbrev=0 upstream/main 2>/dev/null || echo "v8.0.29")
          echo "VERSION=${UPSTREAM_TAG}-p" >> $GITHUB_OUTPUT
          echo "UPSTREAM_TAG=${UPSTREAM_TAG}" >> $GITHUB_OUTPUT
          echo "REPO_LOWER=$(echo '${{ github.repository_owner }}' | tr '[:upper:]' '[:lower:]')" >> $GITHUB_OUTPUT
          echo "COMMIT=$(git rev-parse --short HEAD)" >> $GITHUB_OUTPUT
          echo "BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> $GITHUB_OUTPUT
```

В Dockerfile значение передаётся в аргумент сборки и вшивается компилятором Go:
```dockerfile
ARG VERSION=unknown
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o ./CLIProxyAPI ./cmd/server/
```

**Результат выполнения в контейнере:**
```text
CLIProxyAPI Version: v8.0.29-p, Commit: 2fd6d6ac, BuiltAt: 2026-10-15T02:39:30Z
```
Суффикс `-p` явно указывает, что это сборка с наложенными локальными патчами (`patched`), сохраняя при этом строгий номер базовой версии апстрима.

---

## 4. Автоматическая синхронизация с апстримом

В репозиторий добавлен воркфлоу `.github/workflows/auto-merge-upstream.yml`:

```yaml
name: Auto Merge Upstream Release
on:
  schedule:
    - cron: '0 */6 * * *'  # Каждые 6 часов
  workflow_dispatch:
    inputs:
      ref:
        description: 'Upstream ref (main/dev/tag)'
        default: 'main'
jobs:
  merge:
    runs-on: ubuntu-latest
    steps:
    - uses: actions/checkout@v4
      with:
        token: ${{ secrets.GITHUB_TOKEN }}
        fetch-depth: 0
    - name: Merge upstream
      run: |
        git remote add upstream https://github.com/router-for-me/CLIProxyAPI.git || true
        git fetch upstream --tags
        REF=${{ github.event.inputs.ref || 'main' }}
        git merge upstream/$REF --no-ff -m "auto-merge upstream/$REF @ $(date)"
        git push origin main
```

При появлении нового коммита/релиза в апстриме воркфлоу вливает его в `main`, что немедленно триггерит сборку свежего Docker-образа в GHCR с вычислением нового тега версии (например, `v8.0.30-p`).

---

## 5. Конфигурация и эксплуатация на `onedash2`

### Расположение на сервере
- Каталог проекта: `/opt/docker/CLIProxyAPI`
- Порты сервиса: `:8317` (API), `:8085`, `:1455`, `:54545`, `:51121`, `:11451`
- Веб-панель: `cpa-manager-plus` на `:18317`

### `docker-compose.yml`
```yaml
services:
  cli-proxy-api:
    image: ghcr.io/realmagnum/cli-proxy-api:latest
    container_name: cli-proxy-api
    environment:
      DEPLOY: docker
      MANAGEMENT_PASSWORD: "..."
    ports:
      - "8317:8317"
      - "8085:8085"
      - "1455:1455"
      - "54545:54545"
      - "51121:51121"
      - "11451:11451"
    volumes:
      - /opt/docker/CLIProxyAPI/config.yaml:/CLIProxyAPI/config.yaml
      - /opt/docker/CLIProxyAPI/auths:/root/.cli-proxy-api
      - /opt/docker/CLIProxyAPI/logs:/CLIProxyAPI/logs
    restart: unless-stopped
```

### Скрипт обновления `/opt/docker/CLIProxyAPI/update.sh`
```bash
#!/usr/bin/env bash
set -euo pipefail

cd /opt/docker/CLIProxyAPI

OLD_TAG=$(docker inspect ghcr.io/realmagnum/cli-proxy-api:latest --format '{{index .Config.Labels "org.opencontainers.image.version"}}' 2>/dev/null || echo 'unknown')
echo "==> Pulling latest patched CLIProxyAPI image from GHCR (current: $OLD_TAG)..."
docker compose pull cli-proxy-api

echo "==> Updating container..."
docker compose up -d --force-recreate cli-proxy-api

echo "==> Waiting for service readiness on :8317..."
for i in {1..30}; do
  if curl -sf http://127.0.0.1:8317/ >/dev/null 2>&1 || curl -sf http://127.0.0.1:8317/v1/models >/dev/null 2>&1; then
    echo "==> CLIProxyAPI is up and healthy on :8317!"
    exit 0
  fi
  sleep 1
done

echo "==> Warning: Service did not report ready within 30s. Checking logs:"
docker logs --tail 20 cli-proxy-api
exit 1
```

---

## 6. Верификация работы

### 1. Проверка версии бинарника
```bash
ssh onedash2 'docker exec cli-proxy-api ./CLIProxyAPI --version'
```
Ожидаемый вывод:
```text
CLIProxyAPI Version: v8.0.29-p, Commit: 2fd6d6ac, BuiltAt: 2026-10-15T02:39:30Z
```

### 2. Проверка отдачи метаданных `context_length`
```bash
ssh onedash2 'curl -s http://127.0.0.1:8317/v1/models | grep -E "(context_length|max_context_length)"'
```
Все модели в ответе содержат точные лимиты токенов, что гарантирует корректную работу планировщика агента Hermes.
