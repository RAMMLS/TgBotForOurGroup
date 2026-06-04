# TgBotForOurGroup

Интеграционный сервис на Go, который связывает пользователей Telegram и Discord через Telegram Deep Linking и отправляет уведомления в Telegram-беседы, когда пользователь заходит в голосовой канал Discord.

## Возможности

- Отслеживает `VoiceStateUpdate` в Discord на целевом сервере.
- Хранит связки по схеме `discord_id + telegram_chat_id -> telegram_id`.
- Поддерживает несколько Telegram-бесед одновременно.
- Если в голосовом канале есть привязанный пользователь, создает отдельное Telegram-сообщение в каждой подходящей беседе.
- Раз в 10 минут обновляет это же сообщение и показывает, сколько времени люди сидят в голосовом канале.
- Если связка не найдена или не хватает связки для конкретной беседы, отправляет пользователю в Discord DM только те кнопки привязки, которые относятся к доступным ему беседам.
- Использует одноразовые invite-токены, привязанные к конкретной Telegram-беседе.
- Перед сохранением привязки Telegram-бот проверяет, что пользователь уже состоит именно в той беседе, к которой относится токен.
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
- `TELEGRAM_TARGET_CHAT_ID` — legacy fallback для одной беседы
- `TELEGRAM_TARGET_CHAT_IDS` — основной режим, список chat id через запятую
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
2. Бот проверяет все связки `discord_id + telegram_chat_id` в SQLite.
3. Для каждого Telegram-чата бот строит отдельную видимость.
4. Если в voice-канале присутствуют участники, связанные с конкретной беседой, бот создает или обновляет отдельное сообщение именно в этой беседе.
5. В сообщении показываются:

- название голосового канала;
- сколько времени участники сидят в чате;
- полный список текущих участников, а не только один пользователь.

6. Раз в 10 минут сообщение обновляется автоматически.
7. Если у пользователя нет нужной связки, бот смотрит, какие беседы уже представлены в текущем voice-канале привязанными участниками.
8. В Discord DM пользователь получает только те кнопки привязки, которые относятся к этим беседам.
9. Каждая кнопка ведет на одноразовый Deep Link вида:

```text
https://t.me/<TELEGRAM_BOT_USERNAME>?start=auth_<INVITE_TOKEN>
```

10. Пользователь открывает ссылку, запускает Telegram-бота, бот извлекает invite token и определяет, к какой беседе относится попытка привязки.
11. Если пользователь не состоит в этой беседе, привязка отклоняется.
12. Если пользователь состоит в этой беседе, сохраняется связка `discord_id + telegram_chat_id -> telegram_id`.

## Ограничение Безопасности

Сервис не знает `Telegram User ID` пользователя до момента, пока он не откроет Deep Link в Telegram.

Из-за этого:

- заранее на стороне Discord нельзя надежно определить membership по конкретному `Telegram User ID`;
- зато можно выдавать только chat-scoped invite tokens для бесед, которые уже представлены привязанными участниками в текущем voice-канале;
- и на стороне Telegram можно дополнительно жестко валидировать membership в конкретной беседе.

Текущая реализация делает именно это: даже если посторонний человек получит ссылку, он не сможет привязаться к чужой беседе.

## База данных

При старте создаются таблицы:

```sql
CREATE TABLE IF NOT EXISTS chat_user_mappings (
    discord_id TEXT NOT NULL,
    telegram_chat_id INTEGER NOT NULL,
    telegram_id INTEGER,
    telegram_name TEXT,
    PRIMARY KEY (discord_id, telegram_chat_id)
);

CREATE TABLE IF NOT EXISTS invite_tokens (
    token TEXT PRIMARY KEY,
    discord_id TEXT NOT NULL,
    telegram_chat_id INTEGER NOT NULL,
    inviter_telegram_id INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at INTEGER
);
```

Если раньше использовалась старая таблица `user_mappings`, сервис при старте переносит legacy-данные в новую схему для первого чата из списка.

Файл базы данных создается локально как `bot.db`, либо по пути из `SQLITE_PATH`.

## Docker

Для локального запуска и деплоя без установленного Go используйте Docker Compose.
По умолчанию SQLite теперь хранится в Docker managed volume `tgbotforourgroup-data`, поэтому запуск не зависит от владельца локальной папки `data/` на хосте.

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

Посмотреть Docker volume с базой:

```bash
docker volume inspect tgbotforourgroup-data
```

Если нужно импортировать уже существующий `bot.db` из локальной папки `./data`, остановите сервис и выполните:

```bash
docker compose down
docker run --rm \
  -v tgbotforourgroup-data:/target \
  -v "$(pwd)/data:/source:ro" \
  alpine:3.20 \
  sh -c 'cp /source/bot.db /target/bot.db && chown 10001:10001 /target/bot.db'
docker compose up -d
```

## Бесплатный Деплой

Для этого проекта самый простой бесплатный вариант деплоя на 2026 год — `Oracle Cloud Always Free`, потому что боту нужен долгоживущий процесс без auto-sleep. У Oracle есть Always Free ARM Compute, который можно держать постоянно включенным, а у Render free web services засыпают после простоя, что ломает постоянное Discord Gateway-соединение. Railway для быстрого старта удобен, но бесплатный режим там не выглядит как "навсегда бесплатно". См. официальные страницы: [Oracle Cloud Free Tier](https://www.oracle.com/cloud/free/), [Render Free](https://render.com/docs/free), [Railway Pricing](https://railway.com/pricing).

### Почему это подходит

- Проект уже содержит готовые `Dockerfile` и `docker-compose.yml`.
- Боту не нужен входящий HTTP-трафик, только постоянный исходящий доступ к Discord и Telegram.
- SQLite хранится в Docker volume `tgbotforourgroup-data`, поэтому данные переживут перезапуск контейнера и не зависят от прав владельца на хосте.

### Что важно про время голосовой сессии

Если бот перезапускается во время активного голосового канала, точное историческое время входа уже сидящих пользователей восстановить нельзя через текущий Discord API. Это не критично для обычного деплоя: проблема проявляется только в момент рестарта, и только для уже идущих voice-сессий. После того как канал опустеет и начнутся новые подключения, время снова будет корректным.

### Пошагово: Oracle Cloud Always Free

1. Создайте `Oracle Cloud Always Free` инстанс с `Ubuntu 24.04` и ARM shape.
2. Подключитесь по SSH к серверу.
3. Установите Docker:

```bash
sudo bash deploy/oracle/install-server.sh
```

4. Перезайдите в SSH после добавления пользователя в группу `docker`.
5. Создайте `.env`:

```bash
cp .env.example .env
```

6. Заполните реальные значения токенов и chat ids в `.env`.
7. Запустите бота:

```bash
bash deploy/oracle/deploy.sh
```

8. Проверьте логи:

```bash
docker compose logs -f bot
```

### Обновление после изменений

```bash
bash deploy/oracle/update.sh
```

### Минимальные рекомендации по серверу

- Достаточно одного Always Free ARM VM.
- Открывать наружу для бота нужно только `22/tcp` для SSH.
- Не храните `.env` в публичном репозитории.
- Если хотите совсем без ручных действий после ребута сервера, оставьте `restart: unless-stopped` в `docker-compose.yml` как сейчас.

## Деплой На VDSina 150

Для самого дешевого VPS в `VDSina` лучше запускать этот проект без Docker, чтобы не тратить лишнюю память на контейнерный runtime. Практически это самый дешевый вменяемый сценарий для long-running Discord/Telegram-бота в РФ: тариф `1 vCPU / 1 GB RAM / 10 GB NVMe` начинается примерно от `150 ₽/мес`: [VDSina Standard](https://vdsina.ru/en/pricing/standard).

### Что выбрать в панели VDSina

- Локация: `Москва`.
- ОС: `Ubuntu 24.04`.
- Тариф: `1 CPU / 1 GB RAM / 10 GB NVMe`.
- IPv4: оставить включенным.

### Почему без Docker

- На `1 GB RAM` systemd + один Go-бинарник живут спокойнее.
- SQLite лежит обычным файлом на диске и не требует отдельного сервиса.
- Для этого бота не нужен входящий HTTP, только постоянные исходящие подключения к Telegram и Discord.

### Пошагово

1. Подключитесь к серверу:

```bash
ssh root@YOUR_SERVER_IP
```

2. Установите окружение:

```bash
apt update && apt install -y git
git clone <YOUR_REPOSITORY_URL> /root/TgBotForOurGroup
cd /root/TgBotForOurGroup
sudo bash deploy/vdsina/install-server.sh
```

3. Если хотите уменьшить риск нехватки памяти на самом дешевом тарифе, включите swap:

```bash
fallocate -l 1G /swapfile
chmod 600 /swapfile
mkswap /swapfile
swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab
```

4. Скопируйте проект в рабочую директорию сервиса:

```bash
rm -rf /opt/tgbotforourgroup/app
mkdir -p /opt/tgbotforourgroup
cp -R /root/TgBotForOurGroup /opt/tgbotforourgroup/app
chown -R tgbot:tgbot /opt/tgbotforourgroup
```

5. Создайте `.env`:

```bash
cp /opt/tgbotforourgroup/app/.env.example /opt/tgbotforourgroup/app/.env
sed -i 's#^SQLITE_PATH=.*#SQLITE_PATH=/opt/tgbotforourgroup/data/bot.db#' /opt/tgbotforourgroup/app/.env
nano /opt/tgbotforourgroup/app/.env
```

6. Заполните переменные:

- `DISCORD_BOT_TOKEN`
- `DISCORD_TARGET_GUILD_ID`
- `TELEGRAM_BOT_TOKEN`
- `TELEGRAM_BOT_USERNAME`
- `TELEGRAM_TARGET_CHAT_IDS`
- `SQLITE_PATH=/opt/tgbotforourgroup/data/bot.db`

7. Соберите бинарник:

```bash
cd /opt/tgbotforourgroup/app
/usr/local/go/bin/go build -o /opt/tgbotforourgroup/bin/tgbot ./cmd/bot
chown tgbot:tgbot /opt/tgbotforourgroup/bin/tgbot
```

8. Установите systemd unit:

```bash
cp /opt/tgbotforourgroup/app/deploy/vdsina/tgbotforourgroup.service /etc/systemd/system/tgbotforourgroup.service
systemctl daemon-reload
systemctl enable --now tgbotforourgroup
```

9. Проверьте статус и логи:

```bash
systemctl status tgbotforourgroup --no-pager
journalctl -u tgbotforourgroup -f
```

### Обновление После Изменений

```bash
cd /opt/tgbotforourgroup/app
bash deploy/vdsina/update.sh
```

### Что Важно

- Если бот не стартует, первым делом проверяйте `journalctl -u tgbotforourgroup -n 200 --no-pager`.
- Если сборка падает по памяти, swap почти всегда решает проблему на тарифе `1 GB`.
- Наружу достаточно открыть только `22/tcp` для SSH.
- Файл базы будет лежать в `/opt/tgbotforourgroup/data/bot.db`.

## Vercel

Для этого проекта `Vercel` не подходит как основная платформа деплоя.

Причины:

- Discord-бот использует постоянное Gateway WebSocket-соединение для получения `VoiceStateUpdate`.
- Сервис держит долгоживущий процесс и фоновый таймер, который раз в 10 минут обновляет Telegram-сообщения.
- Serverless-модель Vercel рассчитана на короткие HTTP-вызовы, а не на постоянно живущий бот-процесс.

Для реального деплоя этого бота лучше использовать:

- Docker на VPS;
- любой сервис с long-running containers или processes;
- домашний сервер или отдельную VM.

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
