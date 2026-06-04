# TgBotForOurGroup

Интеграционный сервис на Go, который связывает пользователей Telegram и Discord через Telegram Deep Linking и отправляет уведомления в Telegram-чат, когда пользователь заходит в голосовой канал Discord.

## Возможности

- Отслеживает `VoiceStateUpdate` в Discord на целевом сервере.
- Ищет связку `Discord ID -> Telegram` в SQLite.
- Если связка найдена, отправляет уведомление в целевой Telegram-чат.
- Если связка не найдена, отправляет пользователю в Discord DM с кнопкой на Deep Link Telegram-бота.
- Использует in-memory cooldown на 10 минут, чтобы не спамить DM при прыжках по голосовым каналам.
- Корректно завершает работу по `os.Interrupt` и `SIGTERM`.

## Структура проекта

```text
.
├── cmd/
│   └── bot/
│       └── main.go
├── internal/
│   ├── discord/
│   │   └── discord.go
│   ├── storage/
│   │   └── sqlite.go
│   └── telegram/
│       └── telegram.go
├── .env.example
├── .gitignore
├── go.mod
└── README.md
```

## Зависимости

Используются библиотеки:

- `github.com/bwmarrin/discordgo`
- `gopkg.in/telebot.v3`
- `modernc.org/sqlite`
- `github.com/joho/godotenv`

Если вы поднимаете проект с нуля вручную, команды такие:

```bash
go mod init tgbotforourgroup
go mod tidy
```

Для текущего репозитория достаточно выполнить:

```bash
go mod tidy
```

Если хотите запускать без Docker, используйте Go `1.25+`.
На хостовой машине Go не нужен, если вы запускаете сервис через Docker.

## Настройка

1. Скопируйте пример окружения:

```bash
cp .env.example .env
```

2. Заполните переменные:

- `DISCORD_BOT_TOKEN`
- `DISCORD_TARGET_GUILD_ID`
- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_BOT_USERNAME`
- `TELEGRAM_TARGET_CHAT_ID`
- `SQLITE_PATH` — опционально, по умолчанию в Docker используется `/data/bot.db`

## Discord Intents

Для Discord-бота включите следующие Intents в Discord Developer Portal:

- `Guild Voice States` — обязателен, иначе событие `VoiceStateUpdate` не придет.
- `Server Members` (`Guild Members Intent`) — нужен для корректной работы с участниками сервера и расширения логики.
- `Message Content` — в этом сервисе напрямую почти не используется, но включен по вашему требованию.
- `Presence` (`Guild Presences Intent`) — для текущей логики не обязателен, но также включен по вашему требованию.
- `Guilds` — базовый intent для работы с сервером.

## Как это работает

1. Пользователь заходит в голосовой канал Discord на сервере `DISCORD_TARGET_GUILD_ID`.
2. Бот проверяет `discord_id` в SQLite.
3. Если связка найдена, в Telegram-чат уходит сообщение вида: `👤 <Имя из Telegram> зашел в голосовой канал Discord!`.
4. Если связки нет, бот отправляет пользователю DM с кнопкой:

```text
https://t.me/<TELEGRAM_BOT_USERNAME>?start=auth_<DISCORD_ID>
```

5. Пользователь открывает ссылку, запускает Telegram-бота, бот получает payload `auth_<DISCORD_ID>` и сохраняет связку в таблицу `user_mappings`.

## База данных

При старте создается таблица:

```sql
CREATE TABLE IF NOT EXISTS user_mappings (
    discord_id TEXT PRIMARY KEY,
    telegram_id INTEGER,
    telegram_name TEXT
);
```

Файл базы данных создается локально как `bot.db`, либо по пути из `SQLITE_PATH`.

## Docker

Для локального запуска и деплоя без установленного Go используйте Docker Compose.

Собрать и запустить сервис:

```bash
docker compose up -d --build
```

Посмотреть логи:

```bash
docker compose logs -f bot
```

Остановить сервис:

```bash
docker compose down
```

SQLite хранится в директории `./data`, примонтированной в контейнер как `/data`.

## Запуск

Установите зависимости:

```bash
go mod tidy
```

Запустите сервис:

```bash
go run ./cmd/bot
```

Соберите бинарник при необходимости:

```bash
go build ./cmd/bot
```

## Graceful Shutdown

Сервис корректно обрабатывает сигналы завершения:

- останавливает polling Telegram-бота;
- закрывает Discord session;
- закрывает соединение с SQLite.
