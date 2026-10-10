# SnapFlow

SnapFlow — сервис для хранения и обработки фотографий с API для пользователей и администраторов.

- Регистрация по приглашению и авторизация через JWT.
- Пакетная загрузка изображений, проверка формата и создание миниатюр.
- Шифрование файлов, контроль доступа и пользовательские квоты.
- Асинхронная обработка через RabbitMQ и transactional outbox.
- Идемпотентная загрузка с `Idempotency-Key`, повторы и восстановление задач.
- Метрики и оповещения с Prometheus, Grafana и Telegram; нагрузочные сценарии k6.

Стек: **Go, Gin, PostgreSQL, RabbitMQ, Redis, Docker Compose**, локальное или S3-совместимое хранилище.

## Локальный запуск

Установите Docker Compose и GNU Make, затем создайте конфигурацию:

```powershell
Copy-Item .env.example .env
```

Заполните в `.env` значения `JWT_SECRET`, `ADMIN_NAME`, `ADMIN_EMAIL` и `ADMIN_PASSWORD`, затем выполните:

```shell
make run
```

По умолчанию API доступен на [localhost:8080](http://localhost:8080), Grafana — на [localhost:3000](http://localhost:3000), Mailpit — на [localhost:8025](http://localhost:8025). Порты и учётные данные задаются в `.env`.

## Команды

```shell
make test   # unit-, интеграционные и smoke-тесты
make lint   # golangci-lint
make load   # нагрузочный тест k6
make logs   # логи приложения
make stop   # остановка сервисов
make help   # все команды
```

Контракт API находится в [OpenAPI](api/openapi.yml), примеры запросов — в [коллекции Bruno](api/bruno). Справочная информация: [развёртывание](docs/DEPLOYMENT.md) и [наблюдаемость](docs/OBSERVABILITY.md).
