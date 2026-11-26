<img src="docs/images/ollamapaca-logo.png" width="120" alt="Ollamapaca 标志：黑色线条羊驼和彩色拼图块">

# Ollamapaca

[English](README.md) · **简体中文** · [Español](README.es.md) · [Français](README.fr.md) · [日本語](README.ja.md)

Ollamapaca 是带插件的 Ollama。本地开源模型可以控制你真实的 Chrome 浏览器、使用任意 MCP 服务器，并加载 Codex 和 Claude 插件。一切都在你的电脑上运行，不依赖任何托管服务。

它是 [ollama/ollama](https://github.com/ollama/ollama) 的分支。Mac 应用名为 `Ollamapaca.app`（bundle id `ai.ollamapaca.app`）。它可以和普通的 Ollama 同时安装：直接使用已经在运行的 Ollama 服务器，共用你的模型，但聊天记录独立保存。

配套仓库：[ollama-plugin-seed](https://github.com/richardsondx/ollama-plugin-seed)，包含 Chrome 扩展和本地插件执行器。

## 功能

- **控制你真实的 Chrome。** 通过 Ollamapaca Chrome 扩展，模型可以打开标签页、读取页面、点击、输入、按键和滚动。不需要 `--remote-debugging-port`。
- **像 Codex 和 Claude 一样使用 MCP 服务器。** 在 **Plugins → MCP** 中添加任意本地（stdio）或远程（streamable HTTP 或 SSE）MCP 服务器，也可以直接粘贴 `mcpServers` JSON。
- **Codex 和 Claude 插件。** 支持包含 `.codex-plugin/plugin.json` 或 `.claude-plugin/plugin.json` 的插件文件夹，以及其中自带的 `.mcp.json` 服务器和技能。
- **`@` 提及。** 在聊天中输入 `@browser` 或 `@<MCP 服务器名>`，这条消息就只给模型这些工具。

## 内置插件

以下插件随应用一起提供，在 Plugins 页面启用之前**都是关闭的**。需要密钥或登录的插件会显示设置表单和 **Connect** 按钮。

- 无需任何设置：`cred`（加拿大经济数据）、`exa`（网页和代码搜索）、`firecrawl`（抓取和搜索网页）
- 点击 Connect 登录：`vercel`、`evidence`（也可用 API 密钥）
- 需要密钥：`github`（个人访问令牌）、`dispatch`、`expedia`（还需安装 uv）
- 需要你自己的 Google OAuth 客户端，然后点击 Connect：`gmail`、`google-calendar`、`google-drive`
- 以及 `browser` 和 `computer`

你输入的密钥只保存在你的 Mac 上（`~/.ollamapaca/plugin-settings.json`），登录令牌保存在 `~/.ollamapaca/oauth.json`，两个文件只有你能读取。

先试一个无需设置的：启用 **cred**，然后问 `@cred 自 2023 年以来加拿大 CPI 的同比变化如何？`

## 快速开始（Mac）

1. **构建并打开应用**（需要 Go 1.26 和 Node 22）：

   ```bash
   git clone https://github.com/richardsondx/ollamapaca && cd ollamapaca
   (cd app/ui/app && npm install && npm run build)
   go build -o Ollamapaca ./app/cmd/app && ./Ollamapaca
   ```

   请保持 Ollama 在运行（Ollama 应用或 `ollama serve`），Ollamapaca 会使用它。

2. **获取插件执行器**，需要时应用会自动启动它：

   ```bash
   git clone https://github.com/richardsondx/ollama-plugin-seed ~/.ollamapaca/seed
   ```

3. **加载 Chrome 扩展。** 打开 `chrome://extensions`，开启 **开发者模式**，点击 **加载已解压的扩展程序**，选择 `~/.ollamapaca/seed/chrome-extension`（在文件夹选择窗口中按 Cmd+Shift+G 可以粘贴路径）。扩展弹窗应显示已连接。

4. **启用浏览器插件。** 在 Ollamapaca 中打开 **Plugins**，选择 **browser**，点击 **Enable**。

5. **选择支持工具调用的模型**，例如 `qwen3`、`qwen2.5:7b-instruct`、`llama3.1`、`gpt-oss`，或云端模型如 `glm-5.2:cloud`。Plugins 页面会列出你已安装的模型中哪些支持工具调用。

## 试一试：复制粘贴这些提示词

下面每条提示词都会让模型在你的 Chrome 中操作。你会看到标签页打开，聊天中会逐步显示每个操作（打开、点击、输入），可以展开查看详情，最后给出回答。

**浏览器**

```text
@browser 打开 news.ycombinator.com，告诉我排名第一的新闻
```

```text
@browser 打开 zh.wikipedia.org，搜索“羊驼”，告诉我文章的第一句话
```

```text
@browser 打开 github.com/ollama/ollama，告诉我它有多少颗星
```

```text
@browser 打开 duckduckgo.com，搜索“北京最好的徒步路线”，列出前三个结果
```

```text
@browser 列出我打开的标签页，然后切换到 GitHub 那个
```

**MCP**

在 **Plugins → MCP** 中选择 **Paste JSON**，粘贴以下内容，然后点击 **Add and connect**：

```json
{
  "mcpServers": {
    "memory": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-memory"] }
  }
}
```

显示 **Ready** 后试试：

```text
@memory 记住我最喜欢的动物是羊驼
```

```text
@memory 我最喜欢的动物是什么？
```

远程服务器也一样：选择 **URL** 并输入地址，例如 `https://example.com/mcp`。

**电脑（Mac）**

```text
@computer 列出我可以控制的应用
```

除非开启真实模式，电脑操作都只是模拟运行（dry run）。详见 [docs/PLUGINS-AND-MCP.md](docs/PLUGINS-AND-MCP.md)。

### 用测试来验证

仓库里有一个端到端测试：它通过应用的聊天代码，把真实的提示词发给真实的模型，并使用真实的插件：

```bash
OLLAMAPACA_E2E_MODEL=qwen2.5:7b-instruct OLLAMAPACA_E2E_PLUGINS=browser \
OLLAMAPACA_E2E_PROMPT='@browser go to en.wikipedia.org, search for "Llama", and tell me the first sentence of the article' \
go test ./app/ui -run TestPluginChatEndToEnd -v
```

它会打印每次工具调用、每个结果和模型的回答；如果模型没有调用任何工具，测试就会失败。浏览器类提示词需要插件执行器正在运行，并且 Chrome 扩展已连接。

## 限制

- 页面以文字和带编号的元素列表读取，不会把截图发给模型，暂不列出 iframe 和 shadow DOM 中的内容。
- 当某个工具的描述很长时，小模型有时会写出原版 Ollama 无法解析的工具调用。应用会缩短描述并重试，但根本办法是换用工具调用能力更强的模型。
- 暂无命令行插件命令。

## 默认安全设置

- 插件在你启用之前都是关闭的。未启用的插件不会给模型任何工具。
- 浏览器控制只通过你自己加载的 Chrome 扩展进行，只与 127.0.0.1 通信，并可以在扩展弹窗中关闭。它从不读取 Cookie 或已保存的密码，也从不返回密码框的内容。
- 电脑操作默认是模拟运行。真实控制 Mac 需要设置 `OLLAMA_COMPUTER_USE_REAL=1` 并授予辅助功能权限，有风险的操作仍会要求确认。
- 来自网页和工具的文字只是数据，绝不是指令。它不能添加插件、目标地址或权限。

## 更多

- 完整指南（英文）：[docs/PLUGINS-AND-MCP.md](docs/PLUGINS-AND-MCP.md)
- Ollama 原版文档：见 [README.md](README.md) 下半部分
