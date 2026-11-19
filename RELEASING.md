# Releasing

迷一会儿（Miyo）从 `kiocou/miyo`（`origin`）发布，目前默认构建 Windows 安装包与更新清单。

1. 在干净的工作区确认所有改动已提交，并运行检查：

   ```bash
   go vet ./internal/...
   go test ./internal/...
   ```

2. 更新 `mygo.json` 中的 `version`，并在 `CHANGELOG.md` 顶部添加对应的 `## <version>` 中文说明。MyGo 更新器与 Release 页面会直接读取对应章节。

3. 先推送 `main`，再打并推送 `v<version>` Tag：

   ```bash
   git push origin HEAD:main
   git tag v1.0.0
   git push origin v1.0.0
   ```

4. `.github/workflows/release.yml` 会构建 Windows amd64 / arm64 安装包（`miyo-setup-*-amd64.exe`、`miyo-setup-*-arm64.exe`）、使用 `MYGO_UPDATER_PRIVATE_KEY` 签名更新包并上传到 Draft Release。等待构建完成：

   ```bash
   gh run watch "$(gh run list --repo kiocou/miyo --workflow release.yml -L1 --json databaseId -q '.[0].databaseId')" --repo kiocou/miyo --exit-status
   ```

5. 确认构建产物无误后取消 Draft 并设为 Latest：

   ```bash
   gh release edit v1.0.0 --repo kiocou/miyo --draft=false --latest
   ```

6. Check the update feed shows the new version:

   ```bash
   curl -sL https://github.com/kiocou/miyo/releases/latest/download/update-windows-amd64.json
   ```

## The speech recognition libraries

Podcast captions run sherpa-onnx, whose libraries the app downloads with the model from the `sherpa-onnx-<version>` release, not from the app's own. They only change with sherpa-onnx's version, in `scripts/sherpa`. Then pack them again, commit the `internal/speech/natives.go` it writes, and upload them as a prerelease, which never becomes the latest that updates look for:

```bash
go run ./scripts/sherpa
gh release create sherpa-onnx-2.0.1 --repo kiocou/miyo --prerelease --latest=false build/sherpa/*
```
