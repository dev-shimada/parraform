# parraform

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
terraform自身のロック取得試行（保持中なら失敗する）はそのまま動くが、
parraform自身の`-lock-check`peekはそれとは別に先に実行され、上表の挙動を
そのまま適用する。

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
