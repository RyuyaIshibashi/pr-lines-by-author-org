もちろんです！このツール（`pr-lines-by-author-org`）向けにシンプルで分かりやすい **README.md** を作ってみました。

---

````markdown
# pr-lines-by-author-org

GitHub Organization 配下の全リポジトリを対象に、指定したブランチへマージされた Pull Request を著者別に集計し、**追加行数・削除行数・PR数** を CSV 形式で出力する Go ツールです。  
集計は **GitHub GraphQL API** を利用します。

---

## Features

- **対象ブランチを正規表現で指定可能**  
  デフォルト: `^(master|main|develop|staging|testing)$`
- **全リポジトリ横断で集計**  
  フォークやアーカイブを除外する設定も可能
- **期間フィルタ**  
  `--since` / `--until` でマージ日時の範囲を指定
- **CSV 出力**  
  列: `org,repo,user,additions,deletions,prs`

---

## Installation

```bash
git clone https://github.com/your-org/pr-lines-by-author-org.git
cd pr-lines-by-author-org
go build -o pr-lines-by-author-org
````

---

## Usage

### 1. GitHub Token の準備

環境変数 `GITHUB_ACCESS_TOKEN` に **Personal Access Token (PAT)** を設定してください。
必要なスコープは以下です：

* パブリックリポジトリのみ: `public_repo`
* プライベートリポジトリを含む: `repo`

```bash
export GITHUB_ACCESS_TOKEN=ghp_xxx...
```

### 2. 実行例

```bash
./pr-lines-by-author-org \
  --org your-org \
  --since 2025-08-01T00:00:00+09:00 \
  --until 2025-08-31T23:59:59+09:00 \
  --branches '^(master|main|develop|staging|testing)$' \
  --visibility all \
  --include-forks=false \
  --include-archived=false \
  --max-repos 0 \
  --max-per-branch 1000 \
  --out pr_lines_by_author_org.csv
```

### 3. 出力例（CSV）

```csv
org,repo,user,additions,deletions,prs
your-org,repo-a,alice,1200,300,5
your-org,repo-b,bob,900,200,3
your-org,repo-b,carol,150,50,1
```

---

## Options

| オプション                | 説明                                     | デフォルト                                         |
| -------------------- | -------------------------------------- | --------------------------------------------- |
| `--org`              | 対象の GitHub Organization (必須)           | -                                             |
| `--branches`         | マージ対象のベースブランチを正規表現で指定                  | `^(master\|main\|develop\|staging\|testing)$` |
| `--since`            | 開始日時 (RFC3339 または `YYYY-MM-DD`)        | 指定なし                                          |
| `--until`            | 終了日時 (RFC3339 または `YYYY-MM-DD`)        | 指定なし                                          |
| `--include-forks`    | フォークリポジトリを含めるか                         | `false`                                       |
| `--include-archived` | アーカイブ済みを含めるか                           | `false`                                       |
| `--visibility`       | リポジトリ可視性: `all` / `public` / `private` | `all`                                         |
| `--max-repos`        | 最大リポジトリ数 (0 で無制限)                      | `0`                                           |
| `--max-per-branch`   | リポジトリ×ブランチごとのPR走査上限                    | `1000`                                        |
| `--out`              | 出力CSVファイル (空なら標準出力)                    | -                                             |

---

## Notes

* 集計対象は **PR author** です。コミットの author を集計したい場合は拡張が必要です。
* 大規模リポジトリや期間が長い場合、**GitHub APIのレート制限**に注意してください。
* リネームや自動整形などにより行数が大きく変化するケースもそのままカウントされます。

---
