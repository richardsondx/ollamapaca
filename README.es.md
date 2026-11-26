<img src="docs/images/ollamapaca-logo.png" width="120" alt="Logo de Ollamapaca, una alpaca de líneas negras con una pieza de rompecabezas de colores">

# Ollamapaca

[English](README.md) · [简体中文](README.zh-CN.md) · **Español** · [Français](README.fr.md) · [日本語](README.ja.md)

Ollamapaca es Ollama con plugins. Un modelo abierto y local puede controlar tu Chrome real, usar cualquier servidor MCP y cargar plugins de Codex y Claude, todo en tu equipo, sin ningún servicio alojado.

Es un fork de [ollama/ollama](https://github.com/ollama/ollama). La app para Mac es `Ollamapaca.app` (bundle id `ai.ollamapaca.app`). Funciona junto a una instalación normal de Ollama: reutiliza el servidor de Ollama que ya está en marcha, comparte tus modelos y guarda sus propios chats.

Repositorio complementario: [ollama-plugin-seed](https://github.com/richardsondx/ollama-plugin-seed), con la extensión de Chrome y el ejecutor local de plugins.

## Qué obtienes

- **Control de tu Chrome real.** El modelo abre pestañas, lee páginas, hace clic, escribe, pulsa teclas y se desplaza mediante la extensión de Chrome de Ollamapaca. No hace falta `--remote-debugging-port`.
- **Servidores MCP, como en Codex y Claude.** Añade cualquier servidor MCP local (stdio) o remoto (streamable HTTP o SSE) en **Plugins → MCP**, o pega un fragmento JSON `mcpServers`.
- **Plugins de Codex y Claude.** Se cargan las carpetas con `.codex-plugin/plugin.json` o `.claude-plugin/plugin.json`, con sus servidores `.mcp.json` y sus skills.
- **Menciones con `@`.** Escribe `@browser` o `@<servidor-mcp>` en el chat para dar al modelo solo esas herramientas en un mensaje.

## Plugins incluidos

Todos vienen con la app y están **desactivados hasta que los actives** en la página Plugins. Los que necesitan una clave o iniciar sesión muestran un formulario y un botón **Connect**.

- Sin configuración: `cred` (datos económicos de Canadá), `exa` (búsqueda web y de código), `firecrawl` (extraer y buscar en la web)
- Inicia sesión con Connect: `vercel`, `evidence` (o con clave de API)
- Con clave: `github` (token de acceso personal), `dispatch`, `expedia` (además necesita uv)
- Con tu propio cliente OAuth de Google y luego Connect: `gmail`, `google-calendar`, `google-drive`
- Y además `browser` y `computer`

Las claves que introduces se guardan solo en tu Mac (`~/.ollamapaca/plugin-settings.json`) y los tokens de sesión en `~/.ollamapaca/oauth.json`; solo tú puedes leer ambos archivos.

Prueba uno que no necesita nada: activa **cred** y pregunta `@cred ¿cómo ha cambiado el IPC de Canadá interanualmente desde 2023?`

## Inicio rápido (Mac)

1. **Compila y abre la app**, con Go 1.26 y Node 22:

   ```bash
   git clone https://github.com/richardsondx/ollamapaca && cd ollamapaca
   (cd app/ui/app && npm install && npm run build)
   go build -o Ollamapaca ./app/cmd/app && ./Ollamapaca
   ```

   Mantén Ollama en marcha (la app o `ollama serve`). Ollamapaca lo usa.

2. **Descarga el ejecutor de plugins**, que la app inicia sola cuando hace falta:

   ```bash
   git clone https://github.com/richardsondx/ollama-plugin-seed ~/.ollamapaca/seed
   ```

3. **Carga la extensión de Chrome.** Abre `chrome://extensions`, activa el **Modo de desarrollador**, haz clic en **Cargar descomprimida** y elige `~/.ollamapaca/seed/chrome-extension` (pulsa Cmd+Shift+G en el selector de carpetas para pegar la ruta). Su ventana emergente debería indicar que está conectada.

4. **Activa el plugin del navegador.** En Ollamapaca, abre **Plugins**, selecciona **browser** y haz clic en **Enable**.

5. **Elige un modelo que pueda llamar herramientas**, por ejemplo `qwen3`, `qwen2.5:7b-instruct`, `llama3.1`, `gpt-oss`, o un modelo en la nube como `glm-5.2:cloud`. La página Plugins muestra cuáles de tus modelos instalados pueden llamar herramientas.

## Pruébalo: prompts para copiar y pegar

Cada prompt hace que el modelo actúe en tu Chrome. Verás abrirse la pestaña, y el chat mostrará cada paso (abrir, hacer clic, escribir) con un botón de detalles, y después la respuesta.

**Navegador**

```text
@browser abre news.ycombinator.com y dime cuál es la noticia principal
```

```text
@browser ve a es.wikipedia.org, busca "Alpaca" y dame la primera frase del artículo
```

```text
@browser abre github.com/ollama/ollama y dime cuántas estrellas tiene
```

```text
@browser abre duckduckgo.com, busca "mejores rutas de senderismo cerca de Madrid" y enumera los tres primeros resultados
```

```text
@browser enumera mis pestañas abiertas y cambia a la de GitHub
```

**MCP**

En **Plugins → MCP**, elige **Paste JSON**, pega esto y haz clic en **Add and connect**:

```json
{
  "mcpServers": {
    "memory": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-memory"] }
  }
}
```

Cuando aparezca **Ready**, prueba:

```text
@memory recuerda que mi animal favorito es la alpaca
```

```text
@memory ¿cuál es mi animal favorito?
```

Un servidor remoto funciona igual: elige **URL** e introduce su dirección, por ejemplo `https://example.com/mcp`.

**Ordenador (Mac)**

```text
@computer enumera las apps que puedo controlar
```

Las acciones del ordenador son una simulación salvo que actives el modo real. Consulta [docs/PLUGINS-AND-MCP.md](docs/PLUGINS-AND-MCP.md).

### Demuéstralo con un test

El repositorio incluye un test de extremo a extremo que envía un prompt real a un modelo real a través del código de chat de la app, con los plugins reales:

```bash
OLLAMAPACA_E2E_MODEL=qwen2.5:7b-instruct OLLAMAPACA_E2E_PLUGINS=browser \
OLLAMAPACA_E2E_PROMPT='@browser go to en.wikipedia.org, search for "Llama", and tell me the first sentence of the article' \
go test ./app/ui -run TestPluginChatEndToEnd -v
```

Muestra cada llamada a herramienta, cada resultado y la respuesta del modelo, y falla si el modelo no llamó a ninguna herramienta. Para un prompt de navegador, el ejecutor debe estar en marcha y la extensión de Chrome conectada.

## Limitaciones

- Las páginas se leen como texto y listas numeradas de elementos. No se envían capturas de pantalla al modelo, y todavía no se listan los iframes ni el shadow DOM.
- Los modelos pequeños a veces escriben llamadas a herramientas que Ollama estándar no puede interpretar cuando una herramienta tiene una descripción muy larga. La app acorta las descripciones y reintenta, pero la solución real es un modelo con mejor uso de herramientas.
- Todavía no hay comandos de plugins en la línea de comandos.

## Seguridad por defecto

- Los plugins están desactivados hasta que los activas. Un plugin desactivado no da herramientas al modelo.
- El control del navegador solo funciona a través de la extensión de Chrome que cargas tú, solo habla con 127.0.0.1 y se puede desactivar en su ventana emergente. Nunca lee cookies ni contraseñas guardadas y nunca devuelve el contenido de campos de contraseña.
- Las acciones del ordenador son una simulación por defecto. El control real del Mac requiere `OLLAMA_COMPUTER_USE_REAL=1` y permiso de Accesibilidad, y las acciones arriesgadas siguen pidiendo confirmación.
- El texto de páginas web y herramientas es información, nunca instrucciones. No puede añadir un plugin, un destino ni un permiso.

## Más

- Guía completa (en inglés): [docs/PLUGINS-AND-MCP.md](docs/PLUGINS-AND-MCP.md)
- Documentación original de Ollama: segunda mitad de [README.md](README.md)
