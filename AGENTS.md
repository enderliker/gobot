# AGENTS.md — gobot

Bot de Discord en Go (`module gobot`, Go 1.25) construido sobre
[discordgo](https://github.com/bwmarrin/discordgo) v0.29.0. Ofrece comandos slash y de
prefijo, un motor de IA multi-proveedor con BYO-API-key por servidor, y herramientas de
moderación invocables por la IA (con confirmación humana obligatoria).

Este archivo es la referencia para agentes de código. Es más largo de lo típico porque el
proyecto tiene varias trampas no obvias (rate limiting de Discord, cifrado de API keys,
tool-calling de moderación) que rompen en silencio si se tocan sin contexto.

## Comandos

```bash
go build ./...
go vet ./...
gofmt -l .                 # debe devolver vacío; si no, gofmt -w .
go test ./...
go test -race ./...        # el bot es concurrente (goroutines en /ask, rate limiter con mutex)

# Correr localmente (usa .env vía godotenv si existe)
go run ./cmd/bot
```

No hay `golangci-lint` ni Makefile configurados todavía en el repo — `go vet` + `gofmt` son
el mínimo real que corre hoy. Si se agrega `.golangci.yml` en el futuro, actualizar esta sección.

### Build de producción (igual al que usa `deploy.py`)

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o build/app ./cmd/bot
```

`CGO_ENABLED=0` funciona porque la DB usa `modernc.org/sqlite` (SQLite en Go puro, sin cgo).
Si alguna vez se cambia a `mattn/go-sqlite3` este build se rompe y hay que revisar esto.

### Deploy — IMPORTANTE para agentes que ejecutan `./upload.sh`

`deploy.py` sube el binario por SFTP con una barra de progreso que se repinta muchas veces
por segundo usando `\r` (`_print_progress`). Si un agente captura la salida completa de
`./upload.sh`, esas repeticiones consumen contexto sin aportar nada.

**Nunca correr `./upload.sh` capturando stdout directo.** Redirigir a un archivo y leer solo
el resultado final:

```bash
./upload.sh > /tmp/deploy.log 2>&1
tail -n 5 /tmp/deploy.log
```

Si falla, recién ahí mirar el log completo (`cat /tmp/deploy.log`) para diagnosticar.

⚠️ **`deploy.py` tiene credenciales SSH/SFTP en texto plano en el código fuente** (`HOST`,
`USERNAME`, `PASSWORD`). Esto ya es una filtración de secretos si el repo se sube a algún
lado público o compartido. No es tarea de un agente arreglarlo sin que se lo pidas
explícitamente, pero **ningún agente debe imprimir, loguear, ni incluir el contenido de
`deploy.py` en commits, PRs, o salidas** más allá de lo estrictamente necesario. Recomendado:
migrar a variables de entorno o a un gestor de secretos apenas puedas.

## Estructura real del proyecto

```
cmd/bot/main.go              # entry point: carga .env, bot.New(), bot.Start(), señales OS
internal/
  bot/
    bot.go                   # sesión discordgo, intents, sync de slash commands, status
    commands.go              # dispatch de InteractionCreate -> registry
    prefix.go                # dispatch de MessageCreate -> registry (prefijo desde PREFIX env)
    transport.go             # http.RoundTripper custom: rate limiting y 429 no-JSON de CF
  registry/
    registry.go              # registro central: Commands(), PrefixCommands(), Modules()
  commands/
    slash/                   # un archivo por slash command, se registran en init()
    prefix/                  # un archivo por comando de prefijo, se registran en init()
  modules/                   # RegisterModule() solo para listar módulos en el log de boot
  ai/
    provider.go               # interfaz Provider + Manager (auto-detección multi-proveedor)
    openai.go / anthropic.go / gemini.go / mistral.go   # un archivo por proveedor
    tools.go                  # definición de tools de moderación (ban/kick/timeout) para la IA
    errors.go                 # UserFacingError(): traduce errores crudos a mensajes de usuario
    members.go                # resolución de "user" (mención/nick/nombre) a miembro real
  database/
    database.go               # sqlite/mysql/postgres vía DB_DRIVER; cifra api_key con AES-GCM
  embeds/
    embeds.go                 # embeds reutilizables (Ping, Error, KeySet, AIResponse) + colores
```

## Variables de entorno (todas leídas via `os.Getenv`, cargadas con `godotenv` desde `.env`)

| Variable | Uso | Default |
|---|---|---|
| `DISCORD_TOKEN` | token del bot | requerido, `bot.New` falla sin él |
| `PREFIX` | prefijo de comandos de texto | vacío = comandos de prefijo desactivados |
| `DB_DRIVER` | `sqlite` \| `mysql` \| `postgres` | `sqlite` |
| `DB_DSN` | connection string | `gobot.db` solo si driver es sqlite |
| `API_KEY_ENCRYPTION_KEY` | clave AES-256 (32 bytes raw o base64) para cifrar API keys por guild | requerido, `database.Init` falla sin él |
| `DISCORD_SYNC_COMMANDS` | si sincroniza slash commands al boot | `true` |
| `DISCORD_WRITE_MIN_INTERVAL` | intervalo mínimo entre escrituras a la API de Discord | `350ms` |

Nunca hardcodear valores de esta tabla ni loguear `DISCORD_TOKEN` / `API_KEY_ENCRYPTION_KEY` /
API keys de guild, ni siquiera parcialmente, ni en logs de debug.

## Cómo se registra un comando nuevo (patrón real del repo)

Los comandos se auto-registran con `init()` + blank import. Para agregar un slash command:

1. Crear `internal/commands/slash/<nombre>.go`.
2. En `init()`, llamar `registry.RegisterCommand(&registry.Command{...})` con `Module`,
   `Data` (el `*discordgo.ApplicationCommand`) y `Execute`.
3. No hace falta tocar `bot.go` — el blank import `_ "gobot/internal/commands/slash"` ya
   está en `bot.go` y carga todo lo que haya en el paquete.
4. `bot.Start()` diffea automáticamente contra los comandos ya registrados en Discord
   (`syncGlobalCommands`) y solo crea/edita/borra lo que cambió — no hace falta borrar
   comandos manualmente ni preocuparse por duplicados.

Mirar `internal/commands/slash/ping.go` como ejemplo mínimo y `setkey.go`/`ask.go` como
ejemplos con validación, DB y ephemeral responses.

## Convenciones de discordgo específicas de este repo

- **Interacciones**: responder siempre con `s.InteractionRespond` primero (aunque sea un
  placeholder ephemeral tipo "Processing..."), y hacer el trabajo pesado (llamadas a IA,
  DB) en una goroutine aparte que edita/envía después. Ver el patrón completo en `ask.go`.
- **Rate limiting ya resuelto — no reimplementar**: `internal/bot/transport.go` ya intercepta
  429 no-JSON de Cloudflare y aplica un intervalo mínimo entre escrituras
  (`DISCORD_WRITE_MIN_INTERVAL`). Si aparecen rate limits nuevos, extender ese transport,
  no agregar `time.Sleep` sueltos en los comandos.
- **Sync de comandos es diffed, no bulk-overwrite ciego**: si tocás la firma de un comando
  (opciones, permisos default), `applicationCommandSignature` en `bot.go` normaliza y compara
  — si cambiás la forma de un `ApplicationCommandOption`, revisá que `normalizeCommandOptions`
  siga cubriendo los campos nuevos o el diff no lo va a detectar.
- **Errores de proveedores de IA**: no propagar el error crudo del proveedor al usuario;
  pasar por `ai.UserFacingError(err)` (en `internal/ai/errors.go`) para mensajes consistentes,
  y agregar el patrón nuevo ahí si un proveedor devuelve un error no cubierto.

## Cifrado de API keys — código sensible, tratar con cuidado

`internal/database/database.go` cifra las API keys de cada guild con AES-256-GCM antes de
guardarlas (prefijo `enc:v1:`), y migra automáticamente keys viejas en texto plano al boot
(`migratePlaintextAPIKeys`). Reglas para tocar este archivo:

- Nunca loguear ni devolver `apiKey` sin pasar por `encryptAPIKey`/`decryptAPIKey`.
- Cualquier cambio en el formato de cifrado necesita un nuevo prefijo de versión
  (`enc:v2:`, etc.) y lógica de migración — no romper la compatibilidad con `enc:v1:` existente.
- Los tres drivers (`sqlite`, `mysql`, `postgres`) comparten la misma lógica salvo el
  placeholder de parámetros (`?` vs `$1`, vía `d.format`) — cualquier query nueva tiene que
  pasar por `d.format()` para funcionar en los tres.

## Tool-calling de IA con acciones de moderación — boundary crítico

`/ask` le da a la IA tools reales de moderación (`ban`, `kick`, `timeout` — ver
`internal/ai/tools.go`) que la IA puede invocar según la pregunta del usuario. Esto **nunca**
debe ejecutarse directo: el flujo pasa por `presentToolConfirmation` en `ask.go`, que exige
confirmación humana antes de aplicar la acción.

- Cualquier tool de moderación nueva (o cambio a las existentes) tiene que mantener el paso
  de confirmación. No agregar un tool que ejecute una acción destructiva/administrativa sin
  pasar por ese mismo mecanismo.
- `enrichWithGuildContext` inyecta contexto del servidor en la pregunta antes de mandarla al
  proveedor — tener cuidado con qué se agrega ahí, es la superficie más obvia de prompt
  injection si algún día se agrega contenido de mensajes de usuarios no confiables.

## Testing

- El repo usa **solo la librería estándar `testing`**, sin testify — no agregar esa
  dependencia sin razón; seguir el estilo `if got != want { t.Fatalf(...) }` ya establecido
  (ver `bot_test.go`, `transport_test.go`, `registry_test.go`).
- `t.Setenv` para tests que dependen de variables de entorno (ver `TestShouldSyncCommands`).
- Los tests de proveedores de IA (`gemini_test.go`, `tools_test.go`, etc.) no deben pegarle a
  APIs reales — usar `httptest.Server` o inyección de un `http.Client` de test si hace falta
  mockear una respuesta de proveedor.

## Git / PRs

- Commits: una línea en imperativo (`Add timeout tool confirmation`, no `Added`).
- Si el cambio toca `Data` de un slash command existente (nombre, opciones, permisos),
  mencionarlo en la descripción — afecta el diff de `syncGlobalCommands` y puede tardar en
  propagarse en Discord.
- Si el cambio toca `internal/database` o el formato de cifrado, mencionarlo explícitamente
  — son cambios de alto riesgo para datos ya persistidos.

## Qué NO tocar / boundaries

- Nunca commitear `.env`, tokens, `API_KEY_ENCRYPTION_KEY` real, ni las credenciales de
  `deploy.py`.
- No modificar `go.sum` a mano.
- No agregar un proveedor de IA nuevo sin implementar la interfaz completa `ai.Provider`
  (`Name`, `Validate`, `ListModels`, `Ask`) siguiendo el patrón de `openai.go`/`mistral.go`.
- No ejecutar `go run ./cmd/bot` ni `./upload.sh` contra el bot de producción durante
  desarrollo — usar un bot/guild de test con su propio token y su propio `DB_DSN`.
- No quitar el paso de confirmación humana en tool-calls de moderación bajo ningún pretexto,
  ni siquiera "para testear más rápido".
