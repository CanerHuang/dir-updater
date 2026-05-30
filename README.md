# dir-updater

`updateweb` 是一個小型的網頁式檔案部署工具。啟動後會提供一個上傳頁面，你把壓縮檔（網站靜態檔、前端打包產物等）上傳上去，它會解壓並把內容部署到指定的目標資料夾，用來快速更新某個目錄的內容。

## 功能

- 網頁上傳壓縮檔，自動解壓並部署到目標資料夾
- 支援格式：`.zip`、`.tar.gz`、`.tgz`、`.tar.xz`
- 上傳大小上限 100MB
- 部署前會先清空目標資料夾，再放入新內容（整包覆蓋更新）
- 內建防路徑穿越（path traversal）保護

## 使用方式

啟動後開瀏覽器進入 `http://<host>:<port>/`，選擇壓縮檔上傳即可。

| 參數 | 說明 | 預設 |
| --- | --- | --- |
| `-path` | 部署的目標資料夾（必填） | 無 |
| `-port` | 網頁伺服器連接埠 | `8080` |
| `-version` | 印出版本資訊後結束 | — |

```bash
# -path 為必填，指定要部署的目標資料夾
./updateweb -path /var/www/html

# 自訂連接埠（預設 8080）
./updateweb -path /var/www/html -port 9000

# 查看版本
./updateweb -version
```

## 編譯

```bash
./build.sh            # linux/amd64（預設）
./build.sh --arm64    # linux/arm64
./build.sh --dev      # 版本日期帶到秒（同一天多次建置用）
```

產出位置：`dist/updateweb`
