# parraform

<p align="center">
  <a href="https://github.com/dev-shimada/parraform/actions/workflows/ci.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/dev-shimada/parraform/ci.yml?branch=main&style=flat-square&logo=githubactions&logoColor=white&label=CI"></a>
  <a href="https://codecov.io/gh/dev-shimada/parraform"><img alt="Coverage" src="https://codecov.io/gh/dev-shimada/parraform/branch/main/graph/badge.svg?style=flat-square"></a>
  <a href="https://github.com/dev-shimada/parraform/blob/main/go.mod"><img alt="Go Version" src="https://img.shields.io/github/go-mod/go-version/dev-shimada/parraform?style=flat-square&logo=go&logoColor=white"></a>
  <a href="https://github.com/dev-shimada/parraform/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/github/license/dev-shimada/parraform?style=flat-square"></a>
</p>

<p align="center">
  <a href="https://github.com/dev-shimada/parraform/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/dev-shimada/parraform?style=flat-square&logo=github&logoColor=white"></a>
  <a href="#インストール"><img alt="Homebrew" src="https://img.shields.io/badge/homebrew-dev--shimada%2Fparraform-FBB040?style=flat-square&logo=homebrew&logoColor=white"></a>
</p>

*(English version is the primary document: [README.md](./README.md))*

`terraform` コマンドへの透過ラッパー。`plan` はロックを取得せずに実行し、CIなどで
並列に走る `plan` 同士がロックを取り合って失敗する問題を解消する。`apply` を
含むそれ以外のコマンドは通常通り動作し、排他制御を維持する。

## インストール

### Homebrew

```sh
brew tap dev-shimada/parraform
brew install parraform
```

### Go

```sh
go install github.com/dev-shimada/parraform/cmd/parraform@latest
```

### GitHub Actions

```yaml
- uses: dev-shimada/parraform@v0.1.1
  with:
    version: v0.1.1 # 省略可。省略時は最新リリースを取得する
```

ジョブの以降のステップで`parraform`をPATHに追加する。parraformのみを
インストールするので、実際の`terraform`バイナリも必要な場合は
[`hashicorp/setup-terraform`](https://github.com/hashicorp/setup-terraform)
などと組み合わせて使う。ビルド済みリリースがあるのはLinuxとmacOSの
runnerのみ。それ以外のrunnerでは`go install`でインストールする。

> [!CAUTION]
> `hashicorp/setup-terraform`と組み合わせる場合、**同action自身の**
> `terraform_wrapper`入力は常に`false`にすること。stdout/stderr/exit code
> のキャプチャが必要なら、hashicorpの方ではなく**このaction自身の**
> `terraform_wrapper: true`を使う(後述)。
>
> `parraform`はPATH上で文字通り`terraform`という名前のものにexecする。
> `hashicorp/setup-terraform`の`terraform_wrapper: true`(デフォルト)は
> その名前で独自のNode.js wrapperスクリプトを置くので、parraformは実バイナリ
> ではなくそちらを実行してしまう。execはプロセスの中身をその場で入れ替える
> 操作のため、これはparraformのプロセスの**内部**で、外からは見えない形で
> 起きる——このaction自身の`terraform_wrapper: true`を含め、外側の何も
> 後からこれを検知したり元に戻したりできない。hashicorpのデフォルトの
> ままだと`-detailed-exitcode`が黙って壊れる: 差分ありを意味する exit code
> `2`が、生のプロセスexit codeを見る側(parraform自身、素の`$?`チェック、
> あるいはこのaction自身のwrapperでさえも)から見える時点で`0`になって
> いる。このaction自身のwrapperを有効にした状態でも実際に確認済み:
> hashicorp側の潰しはそれより先に無条件で起きるため、parraform側の
> wrapperでは補正しようがない。

### 出力のキャプチャ(`terraform_wrapper: true`)

```yaml
- uses: hashicorp/setup-terraform@v3
  with:
    terraform_wrapper: false # 上記の注意の通り必須

- uses: dev-shimada/parraform@v0.1.1
  with:
    terraform_wrapper: true

- id: plan
  run: parraform plan -detailed-exitcode
```

このactionにも`terraform_wrapper`という入力があり、`hashicorp/setup-terraform`
の同名入力に名前を合わせている——parraform版の相当品で、parraform
(terraform自体ではなく)をwrapperで包み、stdout・stderr・exit codeを
`stdout`/`stderr`/`exitcode`というoutputとして(このsetupステップではなく、
実際にparraformを呼び出した上の`plan`ステップに)公開する。後続ステップから
`steps.plan.outputs.exitcode`を参照できる。hashicorp側と同様、`0`または`2`の
どちらでもラップした呼び出し自体は成功扱いになる挙動もそのまま踏襲している
が、上記の注意の通りhashicorp側のwrapperを無効にしている限り、`exitcode`は
どちらの場合も実際の値を正しく反映する。デフォルトは`false`
(`hashicorp/setup-terraform`の同名入力のデフォルト`true`とは異なる)。

terraform実行バイナリはPATHから自動的に見つける。別の場所にあるterraformを
使いたい場合は `PARRAFORM_TERRAFORM_BIN` で指定する。

## 使い方

`terraform` の代わりに `parraform` を呼ぶだけでよい。サブコマンド・フラグは
すべてそのままterraformに渡される。

```
parraform init
parraform plan -out=tfplan
parraform apply tfplan
```

CIでは `terraform` という名前でPATHに置いてしまう運用も可能（既存のCI設定を
変えずに導入できる）。

### ロックチェックモード

`plan`実行前のバックエンドロックpeek（[何をしているか](#何をしているか)参照）は
デフォルトでは「警告を表示して実行を継続する」。`-lock-check=strict` を渡すと、
ロックが確認された場合に`plan`自体の実行を拒否し、terraformを一切呼び出さずに
exit 1で終了する:

```
parraform plan -lock-check=strict
```

`-lock-check`はterraform自身のフラグではなくparraform独自のもので、実際の
`terraform`バイナリを呼び出す前に引数から取り除かれる（terraformには渡らない
ため、未知フラグとして拒否されることもない）。意味を持つのは`plan`実行時のみで、
値は`warn`（デフォルト）か`strict`。`-lock`と同様、コマンドライン引数中どこに
現れてもよい。

| `-lock-check` | ロック保持中か | 結果 |
|---|---|---|
| `warn`（デフォルト） | いいえ | plan実行 |
| `warn`（デフォルト） | はい | plan実行、stderrに警告表示 |
| `strict` | いいえ | plan実行 |
| `strict` | はい | plan実行を拒否、exit 1（terraform呼び出しなし） |

これは`-lock`とは独立している: 明示的に`-lock=true`を指定していても、
terraform自身は通常通りロック取得を試みる（`-lock-timeout`を設定していれば
即座に失敗せず待機する）が、parraform自身の`-lock-check`peekはそれとは別に
先に実行され、上表の挙動をそのまま適用する。ロック保持中に`-lock-check=warn`
（デフォルト）でplanを通す場合、警告文言もこのケースに合わせて調整される
— 明示的に`-lock=true`を指定しているのに「unlockedで実行する」と表示する
ことはない。

parraformを`terraform`の差し替えとして動かしていて、CLIフラグを追加できず
環境変数しか設定できない環境（Atlantis、terragrunt）では、`-lock-check`は
`TF_CLI_ARGS_plan`経由でも指定できる。例: `TF_CLI_ARGS_plan=-lock-check=strict`。
同様にその変数からも取り除かれる（理由も同じ）。コマンドラインで直接
`-lock-check`を指定した場合は、`TF_CLI_ARGS_plan`側の指定より常に優先される。

内部のpeekはbest-effort: 非対応バックエンド、peekのタイムアウト・失敗、
`PARRAFORM_LOCK_CHECK_TIMEOUT`が`0`以下（チェック自体を無効化）のいずれも
「ロック未確認」として扱われる。つまり`strict`は実際に観測できたロックにのみ
反応し、確信が持てない場合にブロックすることはない。

### シェル補完

cobraベースなので bash/zsh/fish/powershell の補完スクリプトを生成できる。

```
parraform completion bash > /etc/bash_completion.d/parraform
```

## 何をしているか

- `plan` 実行時のみ `TF_CLI_ARGS_plan` に `-lock=false` を追加し、ロックを
  取得せずに実行する。ユーザーが明示的に `-lock=true`/`-lock=false` を
  コマンドラインで指定した場合はそちらが優先される。
- `plan` 実行前に、設定されているバックエンドの実ロック状態を読み取り専用で
  確認し、他プロセスが保持中であれば警告を表示する。デフォルトでは`plan`自体
  は止めないが、`-lock-check=strict` を指定するとロック確認時に`plan`実行を
  拒否するようになる — [ロックチェックモード](#ロックチェックモード)参照。
- `apply` / `import` / `refresh` / `state mv` など、state を書き換える
  コマンドは一切変更しない完全パススルー。通常通りロックを取得・チェックする。
- `terraform` の実バイナリへ `syscall.Exec`（Unix）でプロセス置換するため、
  stdio・TTY判定・シグナル・終了コードは直接terraformを実行した場合と
  完全に同一。

## なぜ安全か

`-lock=false` でのplanが安全な理由: S3/GCS等への書き込みはアトミックで
read-after-write一貫性があるため、apply中のplanが「壊れた」状態を読む
（torn read）ことはない。最悪でも「apply完了直前のスナップショットを読む」
だけであり、破損は起きない。

さらに実機で検証済み: `plan -out=` で保存したplanファイルは、保存後に
別の操作でstateが変わっていると `apply <planfile>` 実行時に
`Error: Saved plan is stale` で明示的に失敗する。つまり「CIでplanを保存し、
別ジョブでapplyする」という典型的なワークフローでも、unlocked planが多少
古いstateを読んでいた場合はapply時にfail-closedし、古い前提でのapplyが
誤って実行されることはない。

バックエンドロックのpeekはこれに加えて「apply進行中である」ことを利用者に
知らせるための追加シグナルである。デフォルトでは`plan`の実行そのものを
止めることはないが、「変わるかもしれないstateに対して実行するよりは
早く失敗してリトライしたい」場合のために、`-lock-check=strict` で
あえて止める挙動も選べる（[ロックチェックモード](#ロックチェックモード)参照）。

## ロックチェックの対象バックエンド

terraformのremote stateとして設定可能な全11種別（`local`除く）にロック
チェックを実装している。検証の深さには差があり、以下の3段階がある。

| 信頼度 | 内容 | 対象 |
|---|---|---|
| 高 | terraform本体のソースを確認した上で、実際のSDKが送信するHTTPリクエストをhttptest（kubernetesのみclient-goのfake clientset）で検証済み | local, s3, gcs, azurerm, consul, kubernetes, cos, oci |
| 中 | terraform本体のソースは確認済みだが、実際のDB/インスタンスに対する統合テストは未実施（ワイヤプロトコルがhttptestで模擬しづらいため） | pg, oss |
| 範囲限定 | ソース確認・実機検証済みだが対応範囲を絞っている | remote / cloud（TFC/TFE。`workspaces.name`固定のみ対応、`tags`/`project`による動的ワークスペース解決は非対応） |

`http` backendはterraform自身のcontractにpeek用のAPIが存在しないため対応
不可（`-lock=false`のみ適用され、ロックチェックはスキップされる）。

いずれのbackendでも、設定の抽出やロック機構の理解に確信が持てない場合は
「間違ったロック識別子で誤った警告を出す/出さない」ことを避け、チェックを
黙ってスキップする（`-lock=false`だけを適用してplanは実行する）設計にして
いる。

## 環境変数

| 変数 | 説明 |
|---|---|
| `PARRAFORM_TERRAFORM_BIN` | 使用するterraformバイナリのパスを明示指定する |
| `PARRAFORM_LOCK_CHECK_TIMEOUT` | ロックpeekのタイムアウト（Goのduration文字列、デフォルト`3s`）。`plan`実行のたびに必ず加算される待ち時間であるため、クラウド認証チェーンの解決が遅い環境では調整するとよい。`0`以下でチェック自体を無効化する |

## 開発

```
go build ./...
go test ./...
golangci-lint run ./...
```

docker を使ったテストが2種類用意されている。どちらも`integration`ビルド
タグの裏に置いてあるため、上記のコマンドではdockerもterraformバイナリも
一切必要としない:

- S3バックエンドのロックチェッカーには、実際のS3/DynamoDB互換サーバー
  （[ministack](https://github.com/ministackorg/ministack)）を使った統合
  テストがある:

  ```
  go test -tags=integration ./internal/lockcheck/... -run TestS3Integration -v
  ```

- 実際の`parraform`バイナリをビルドし、実際の`terraform`バイナリと
  ministackに対して動かすE2Eテストがある。実terraformの`plan`は保持中の
  state lockでブロックされる一方、`parraform plan`はブロックされないこと、
  さらに`parraform plan`を多数同時実行してもロックを取り合わないことを
  検証している:

  ```
  go test -tags=integration ./cmd/parraform/ -run TestE2E -v
  ```

いずれもdocker（E2Eテストはterraformも）が未インストールの場合は自動的に
スキップされる。

CIはpush/pull requestのたびに同じ3つのチェックを実行する（build/testは
Linux/macOS/Windowsの3プラットフォーム）に加え、Linuxでは上記2つの統合
テストも実行する。リリースは`v*`タグをpushすると
[GoReleaser](https://goreleaser.com/)がビルドしてGitHub Releasesと
[homebrew-parraform](https://github.com/dev-shimada/homebrew-parraform)
tapに公開する。
