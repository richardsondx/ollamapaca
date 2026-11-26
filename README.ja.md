<img src="docs/images/ollamapaca-logo.png" width="120" alt="Ollamapaca のロゴ。黒い線画のアルパカとカラフルなパズルのピース">

# Ollamapaca

[English](README.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · **日本語**

Ollamapaca はプラグイン付きの Ollama です。ローカルのオープンモデルが、あなたの実際の Chrome を操作し、任意の MCP サーバーを使い、Codex や Claude のプラグインを読み込めます。すべてあなたのマシン上で動き、ホスト型サービスは使いません。

[ollama/ollama](https://github.com/ollama/ollama) のフォークです。Mac アプリは `Ollamapaca.app`（bundle id `ai.ollamapaca.app`）です。通常の Ollama と同時にインストールできます。すでに動いている Ollama サーバーをそのまま使い、モデルは共有し、チャット履歴は別に保存します。

関連リポジトリ：[ollama-plugin-seed](https://github.com/richardsondx/ollama-plugin-seed)（Chrome 拡張機能とローカルのプラグイン実行サーバー）。

## できること

- **実際の Chrome を操作。** Ollamapaca の Chrome 拡張機能を通じて、モデルがタブを開き、ページを読み、クリック、入力、キー操作、スクロールを行います。`--remote-debugging-port` は不要です。
- **Codex や Claude と同じように MCP サーバーを利用。** **Plugins → MCP** で、ローカル（stdio）やリモート（streamable HTTP または SSE）の MCP サーバーを追加できます。`mcpServers` の JSON を貼り付けることもできます。
- **Codex と Claude のプラグイン。** `.codex-plugin/plugin.json` や `.claude-plugin/plugin.json` を含むフォルダを、同梱の `.mcp.json` サーバーやスキルごと読み込みます。
- **`@` メンション。** チャットで `@browser` や `@<MCP サーバー名>` と入力すると、そのメッセージではモデルにそのツールだけを渡します。

## クイックスタート（Mac）

1. **アプリをビルドして起動します**（Go 1.26 と Node 22 が必要）：

   ```bash
   git clone https://github.com/richardsondx/ollamapaca && cd ollamapaca
   (cd app/ui/app && npm install && npm run build)
   go build -o Ollamapaca ./app/cmd/app && ./Ollamapaca
   ```

   Ollama（アプリまたは `ollama serve`）は起動したままにしてください。Ollamapaca がそれを使います。

2. **プラグイン実行サーバーを取得します。** 必要になるとアプリが自動で起動します：

   ```bash
   git clone https://github.com/richardsondx/ollama-plugin-seed ~/.ollamapaca/seed
   ```

3. **Chrome 拡張機能を読み込みます。** `chrome://extensions` を開き、**デベロッパー モード** をオンにして **パッケージ化されていない拡張機能を読み込む** をクリックし、`~/.ollamapaca/seed/chrome-extension` を選びます（フォルダ選択画面で Cmd+Shift+G を押すとパスを貼り付けられます）。ポップアップに接続済みと表示されれば OK です。

4. **ブラウザプラグインを有効にします。** Ollamapaca で **Plugins** を開き、**browser** を選んで **Enable** をクリックします。

5. **ツール呼び出しに対応したモデルを選びます。** 例：`qwen3`、`qwen2.5:7b-instruct`、`llama3.1`、`gpt-oss`、またはクラウドモデルの `glm-5.2:cloud`。インストール済みのどのモデルがツールを呼び出せるかは Plugins ページに表示されます。

## 試してみる：コピーして貼り付けるプロンプト

以下のプロンプトを送ると、モデルがあなたの Chrome で操作します。タブが開くのが見え、チャットには各ステップ（開く、クリック、入力）が詳細ボタン付きで表示され、最後に回答が出ます。

**ブラウザ**

```text
@browser news.ycombinator.com を開いて、トップの記事を教えて
```

```text
@browser ja.wikipedia.org を開いて「アルパカ」を検索し、記事の最初の一文を教えて
```

```text
@browser github.com/ollama/ollama を開いて、スターの数を教えて
```

```text
@browser duckduckgo.com を開いて「東京近郊のおすすめハイキングコース」を検索し、上位3件を挙げて
```

```text
@browser 開いているタブを一覧にして、GitHub のタブに切り替えて
```

**MCP**

**Plugins → MCP** で **Paste JSON** を選び、次の内容を貼り付けて **Add and connect** をクリックします：

```json
{
  "mcpServers": {
    "memory": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-memory"] }
  }
}
```

**Ready** と表示されたら、次を試してください：

```text
@memory 私の好きな動物はアルパカだと覚えておいて
```

```text
@memory 私の好きな動物は何？
```

リモートサーバーも同じです。**URL** を選んでアドレス（例：`https://example.com/mcp`）を入力します。

**コンピューター（Mac）**

```text
@computer 操作できるアプリを一覧にして
```

実行モードをオンにしない限り、コンピューター操作はドライラン（模擬実行）です。詳しくは [docs/PLUGINS-AND-MCP.md](docs/PLUGINS-AND-MCP.md) を参照してください。

### テストで確かめる

リポジトリには、アプリのチャット処理を通して本物のプロンプトを本物のモデルに送り、本物のプラグインを使うエンドツーエンドテストがあります：

```bash
OLLAMAPACA_E2E_MODEL=qwen2.5:7b-instruct OLLAMAPACA_E2E_PLUGINS=browser \
OLLAMAPACA_E2E_PROMPT='@browser go to en.wikipedia.org, search for "Llama", and tell me the first sentence of the article' \
go test ./app/ui -run TestPluginChatEndToEnd -v
```

各ツール呼び出し、各結果、モデルの回答を表示し、モデルがツールを一度も呼ばなかった場合は失敗します。ブラウザ系のプロンプトでは、実行サーバーが起動していて Chrome 拡張機能が接続されている必要があります。

## 制限事項

- ページはテキストと番号付きの要素リストとして読み取ります。スクリーンショットはモデルに送らず、iframe や shadow DOM の中身はまだ一覧に出ません。
- ツールの説明がとても長いと、小さなモデルが標準の Ollama で解析できないツール呼び出しを書くことがあります。アプリは説明を短くして再試行しますが、根本的な解決はツール呼び出しに強いモデルを使うことです。
- コマンドラインのプラグインコマンドはまだありません。

## 安全のための初期設定

- プラグインは有効にするまでオフです。無効なプラグインはモデルにツールを渡しません。
- ブラウザ操作は、あなたが自分で読み込んだ Chrome 拡張機能経由でのみ行われ、通信先は 127.0.0.1 だけで、ポップアップからオフにできます。Cookie や保存済みパスワードは読まず、パスワード欄の値を返すこともありません。
- コンピューター操作は初期設定ではドライランです。Mac を実際に操作するには `OLLAMA_COMPUTER_USE_REAL=1` とアクセシビリティの許可が必要で、リスクのある操作は引き続き確認を求めます。
- Web ページやツールからのテキストはデータであり、指示ではありません。プラグイン、宛先、権限を追加することはできません。

## 詳しく

- 完全なガイド（英語）：[docs/PLUGINS-AND-MCP.md](docs/PLUGINS-AND-MCP.md)
- Ollama 本来のドキュメント：[README.md](README.md) の後半
