# QQ-bot
用 go 写的 QQ Bot，慢慢添加常用功能。
> 已从 LLOneBot 迁移到 [NapCat](https://napcat.napneko.icu/) 框架，接口规范参考 [NapCat 接口文档](https://napcat.apifox.cn/)。两端都是 OneBot 11 实现，常用接口完全兼容。
> 部署支持 Windows 本地直跑 + Linux Docker (CI/CD: 打 tag 一键部署)。

## 目前主要功能：
* **jm 番号 章节[默认为1]**（返回pdf）
* **pix 关键词[可留空，r18] r18[默认留空]**（返回图片）
* **help 功能[jm,pix等]**（返回指令使用格式）
* **github**（返回开源仓库链接）
* **任意其他文本**：默认转发给 Kimi (Moonshot AI) 用「牧濑红莉栖」人设回复（可选，需配 `GPT_API_KEY` / `MOONSHOT_KEY`；不配就回退到打印菜单）

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=xiao-en-5970/QQ-bot&type=Date)](https://www.star-history.com/#xiao-en-5970/QQ-bot&Date)

---

## 一、本地启动（Windows / Mac / Linux）

### 0. 准备 NapCat 运行环境
跟随官方文档安装 NapCat（推荐 Docker 或 Shell 一键脚本）：
> [NapCat 安装指南](https://napcat.napneko.icu/guide/start-install)

### 1. 在 NapCat 中开启 OneBot11 HTTP 服务
- 启动 NapCat 后用机器人账号登录
- WebUI -> 网络配置 -> 启用 **HTTP server**，端口 `3000`
- 设置 token，记录下来填到 `test.yaml` 的 `server.access_token`

> 注意：NapCat 修改完 OB11 配置后**必须重启容器**让配置生效，否则 HTTP server 可能只 bind 在容器内 loopback：
> ```bash
> docker restart napcat
> ```

### 2. 准备 Go 1.22 环境
> [Go 下载](https://golang.google.cn/dl/)

### 3. 安装外部 Python 工具（jm / pdf 命令需要）

| 平台 | 命令 |
|---|---|
| Windows | 仓库已自带 `package/jmcomic.exe` 与 `package/img2pdf.exe`，无需额外安装 |
| Linux / Mac | `pip install jmcomic img2pdf pillow pyyaml`（或在 venv 中装） |

非 Windows 平台启动时会自动调用 PATH 中的 `jmcomic` / `img2pdf` 命令；如果你装在了 venv 里，需要先 `source .venv/bin/activate`。

### 4. 配置 `./test.yaml`

```yaml
log:
  std_out_log_level: debug
  log_level: debug
  log_file: ./logs/qq-bot.log

server:
  address: "https://bot-http.xiaoen.xyz/" # 末尾必须带 /
  access_token: ""                          # NapCat HTTP token

pixiv:
  pixiv_address: "https://image.anosu.top/pixiv/json"
  size: origin

group:
  group_id:                                 # 留空则自动从账号拉取
  get_group_history_interval: 3
  update_group_list_interval: 1200
  retry: 3

user:
  user_id:                                  # 留空则自动获取当前 bot 账号

cache:
  # 留空 -> 系统临时目录/qq-bot/{jm,pdf}（Linux: /tmp/qq-bot/...；Windows: %TEMP%/qq-bot/...）
  tmp_dir: ""
  pdf_tmp_dir: ""
  max_size: 1024
  clear_interval: 1200

tools:
  jmcomic_bin: ""    # 留空按 OS 自动选择，详见 conf/conf.go
  img2pdf_bin: ""
  python_bin: ""

gpt:
  api_key: ""        # 推荐用环境变量 GPT_API_KEY（也兼容老的 MOONSHOT_KEY）
  max_context_size: 40
  system_prompt: ""  # 留空使用代码内置的牧濑红莉栖人设
```

> **配置优先级**：环境变量 > test.yaml > 代码默认值。生产部署只用 env，YAML 留给本地开发。

### 5. 启动项目
```shell
# 直接启动
go run main.go
# 从命令行获取群号
go run main.go GROUP_ID=xxxxxxxxx
```

启动时会调 `/get_status` 做连通性检查，失败会打 warning 但仍继续运行。

### 6. 观察结果
群里 `@bot jm 350234`，看是否成功返回并上传 pdf。

---

## 二、Linux 服务器部署 (Docker + CI/CD)

完整部署文档：[`deploy/README.md`](./deploy/README.md)。

### 速览

```
打 tag (v1.0.0) ──> GitHub Actions ──> 静态分析 ──> 编译 build/app
                                            │
                                            └─> rsync 到部署机
                                                    │
                                                    └─> docker build & docker run
                                                            └─ 容器名: qq-bot-server
                                                               无端口映射（轮询型 bot）
                                                               --env-file /qq-bot-server/.env
                                                               -v /qq-bot-server/cache:/tmp/qq-bot   # 一次挂出全部缓存
```

部署流程模仿了 [HFUT-Graduation-Project](../HFUT-Graduation-Project) 的模式（Actions 跨平台编译 + 部署机本地 Docker build）。

### 一键打 tag 触发部署

```bash
Tag=1.0.0 make tag        # 自动补 v 前缀，相当于 git tag v1.0.0 && git push origin v1.0.0
```

### 首次部署需要做的事（一次性）

1. 服务器装 Docker、确保 NapCat 已经在跑
2. 服务器创建 `/qq-bot-server/.env`（参考 [`deploy/env.production.example`](./deploy/env.production.example)）和 `/qq-bot-server/cache/{jm,pdf,logs}/` 持久化目录
3. GitHub 仓库配 4 个 Secrets：`DEPLOY_HOST` / `DEPLOY_USER` / `DEPLOY_SSH_KEY`(或 `_B64`) / `DEPLOY_PATH`(可选)
4. GitHub Settings → Environments → 新建 `production`，加自己作为 reviewer

详细步骤见 [`deploy/README.md`](./deploy/README.md)。

---

## 用到的 NapCat 接口（均与 OneBot11 兼容）

| 接口 | 用途 |
| --- | --- |
| `/get_status` | 启动期连通性检查 |
| `/get_login_info` | 自动获取 bot 自己的 QQ |
| `/get_group_list` | 自动发现监听群、定期刷新 |
| `/get_group_msg_history` | 轮询群消息识别 @bot 指令 |
| `/send_group_msg` | 发送群文本 / @ 提示 |
| `/upload_group_file` | 上传 pdf / 图片到群文件 |
| `/can_send_image` | pix 命令前的能力检查 |

完整接口规范见 [NapCat 接口文档](https://napcat.apifox.cn/)。

## 其他配置
### 配置 `./package/jmoption/opt.yml`
> 配置参考 [option常规配置项](https://jmcomic.readthedocs.io/zh-cn/latest/option_file_syntax/)

## 感谢以下项目
* **bot 框架** [NapCat](https://github.com/NapNeko/NapCatQQ)
* **jm 抓取本子** [JMComic-Crawler-Python](https://github.com/hect0x7/JMComic-Crawler-Python)
* **图片合 pdf** [img2pdf](https://gitlab.mister-muffin.de/josch/img2pdf) / [salikx/image2pdf](https://github.com/salikx/image2pdf)（Linux 内存优化参考）
* **AI 聊天** [Moonshot Kimi API](https://platform.moonshot.cn/) via [northes/go-moonshot](https://github.com/northes/go-moonshot)

## 最后
**项目还在不断完善，期待更多功能的加入**
