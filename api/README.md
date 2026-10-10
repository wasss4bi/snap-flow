# API SnapFlow

Swagger UI (`/swagger/` и его ресурсы) и спецификация (`/openapi.yml`) доступны только администратору после входа через `POST /api/login`. Поддерживаются cookie `auth_token` и `Authorization: Bearer <JWT>`. Без входа возвращается 401, обычному пользователю — 403. Права проверяются по базе при каждом запросе; logout отзывает текущий токен.

В проекте пока нет страницы входа. Чтобы открыть Swagger в браузере, откройте `/` приложения, затем выполните в консоли разработчика:

```javascript
const email = prompt('Email администратора');
const password = prompt('Пароль администратора');
const response = await fetch('/api/login', {
  method: 'POST',
  headers: {'Content-Type': 'application/json'},
  body: JSON.stringify({email, password}),
});
if (response.ok) location.href = '/swagger/';
else console.error('Вход не выполнен:', response.status);
```

Браузер автоматически сохранит cookie из ответа. При доступе к серверу используйте HTTPS или SSH-туннель. `ADMIN_EMAIL` и `ADMIN_PASSWORD` задают данные для создания первого администратора, но изменение этих переменных не меняет пароль уже созданной учётной записи.

Swagger UI доступен по адресу http://localhost:8083/swagger/ при текущем HTTP_PORT, спецификация — `/openapi.yml`.

Администратор входит через `POST /api/login`, затем вызывает `POST /admin/invites` с `access_to` и `memory_limit`. Приглашение действительно 24 часа. Пользователь передаёт его как `invite_code` при регистрации и подтверждает адрес через `POST /api/verify-email`, указав `email` и шестизначный `code`. После входа JWT передаётся в `Authorization: Bearer ...`.

`GET /admin/users` показывает пользователей и лимиты; `PUT /admin/users/{id}/limits` изменяет срок и объём. `GET /profile` возвращает текущие ограничения пользователя. Все даты задаются в формате RFC 3339 с часовым поясом, объём — в байтах.

Коллекция Bruno обновлена для нового контракта; запускайте fixtures только на локальном тестовом стенде. Скрипт Test-Recovery пока относится к предыдущему контракту.
