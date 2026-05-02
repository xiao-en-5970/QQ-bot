# QQ-bot 部署说明

> 仿 HFUT-Graduation-Project 的 CI/CD 模式：main 分支只跑 `go vet`；打 `v*` tag 触发部署到 Linux 服务器。

## 服务器首次配置

### 1. 安装 Docker

```bash
curl -fsSL https://get.docker.com | sh
sudo systemctl enable docker
sudo systemctl start docker
```

### 2. 准备宿主机目录

```bash
sudo mkdir -p /qq-bot-server/cache/{jm,pdf,logs}
sudo chmod -R 755 /qq-bot-server
```

| 宿主机路径                          | 容器路径              | 用途                                        |
|--------------------------------|-------------------|-------------------------------------------|
| `/qq-bot-server/.env`          | (--env-file)        | 运行时环境变量，**敏感信息不入镜像**                      |
| `/qq-bot-server/cache`         | `/tmp/qq-bot`       | 一次挂出全部缓存（jm/pdf/logs 都在这里），不污染容器工作目录 `/app` |
| `/qq-bot-server/cache/jm`      | `/tmp/qq-bot/jm`    | jm 下载图片缓存                                  |
| `/qq-bot-server/cache/pdf`     | `/tmp/qq-bot/pdf`   | jm 章节合成 pdf 缓存                             |
| `/qq-bot-server/cache/logs`    | `/tmp/qq-bot/logs`  | zap 日志文件，便于宿主机 tail 看运行情况                  |

### 3. 配置宿主机 .env

把项目里的 `deploy/env.production.example` 复制到宿主机 `/qq-bot-server/.env`，按实际值填写：

```bash
sudo cp /path/to/repo/deploy/env.production.example /qq-bot-server/.env
sudo chmod 600 /qq-bot-server/.env
sudo nano /qq-bot-server/.env  # 把 SERVER_ACCESS_TOKEN 等填进去
```

> **重要**：`.env` 含 NapCat token，不提交到 Git。CI 部署时会过滤注释行，仅 `KEY=VALUE` 注入容器。

### 4. NapCat 必须先正常运行

QQ-bot 容器启动时会调 `${SERVER_ADDRESS}/get_status` 做连通性检查，所以确保：

- NapCat WebUI -> 网络配置 -> HTTP server 已开启，端口 3000，token 与 `SERVER_ACCESS_TOKEN` 一致
- `bot-http.xiaoen.xyz` 反代 -> NapCat 容器 3000 端口已通（容器外可 `curl https://bot-http.xiaoen.xyz/get_status`）
- 如果 NapCat 与 QQ-bot 在同一台机器：建议把 `SERVER_ADDRESS` 改成 `http://172.17.0.1:3000/`（docker 网桥网关），不走公网

### 5. 配置 GitHub Secrets

仓库 **Settings** → **Secrets and variables** → **Actions** → **New repository secret**：

| Secret 名称        | 必填 | 说明                                                                         |
|--------------------|------|----------------------------------------------------------------------------|
| `DEPLOY_HOST`      | ✓ | 服务器 IP，例如 `47.94.197.213`                                                  |
| `DEPLOY_USER`      | ✓ | SSH 用户名，例如 `root`                                                          |
| `DEPLOY_SSH_KEY`   | ✓* | 私钥完整内容（含 BEGIN/END 行）；或用 `DEPLOY_SSH_KEY_B64`                              |
| `DEPLOY_SSH_KEY_B64` | * | 私钥的 Base64 编码：`base64 -w 0 ~/.ssh/deploy_key`                            |
| `DEPLOY_PATH`      | 可选 | 远程仓库 rsync 目标路径，默认 `~/qq-bot-server`                                       |

### 6. 在服务器添加部署公钥

本机生成专用密钥对（仅用于部署）：

```bash
ssh-keygen -t ed25519 -C "qq-bot-deploy" -f ~/.ssh/deploy_key -N ""
cat ~/.ssh/deploy_key.pub
```

SSH 登录服务器，将公钥追加到 `~/.ssh/authorized_keys`：

```bash
echo "ssh-ed25519 AAAA... qq-bot-deploy" >> ~/.ssh/authorized_keys
chmod 600 ~/.ssh/authorized_keys
```

把 `~/.ssh/deploy_key`（私钥）完整复制到 GitHub Secret `DEPLOY_SSH_KEY`。

---

## CI/CD 流程

### Push 到 main 分支

只跑 **静态分析**（`go vet ./...`），不编译、不构建、不部署。

### 打 tag（例如 v1.0.0）

触发完整流水线：**静态分析 → Actions 编译 build/app → rsync 到部署机 → docker build → docker run**。

```bash
# 推荐用 Makefile 一键 tag（自动补 v 前缀）
Tag=1.0.0 make tag
# 等价于
git tag v1.0.0 && git push origin v1.0.0
```

### 手动审批（首次需要在 Settings 配 Environment）

流水线在「部署」步骤前需要审批：

1. **Settings → Environments → New environment** -> 命名为 `production`
2. 勾选 **Required reviewers**，把自己加进去，**Save protection rules**
3. 之后每次 tag 触发部署时，到 **Actions** → 选择 run → **Review deployments** → **Approve and deploy**

---

## 容器细节

容器名固定为 `qq-bot-server`。启动命令（CI 自动执行，等价手动版）：

```bash
docker run -d --name qq-bot-server \
  --restart unless-stopped \
  --env-file /qq-bot-server/.env \
  -v /qq-bot-server/cache:/tmp/qq-bot \
  qq-bot-server:latest
```

注意：QQ-bot 是**轮询型 bot**，不接收外部请求，**不开放任何端口**。pprof 只监听容器内 localhost:6060。

镜像里已经预装：

- 预编译好的 Linux Go 二进制 `/app/app`
- Python 3 + jmcomic + img2pdf + pillow + pyyaml（位于 `/opt/qq-bot-venv/bin`，加入 PATH）
- jm 配置 `/app/package/jmoption/`

---

## 常见问题

### 1. 容器启动后立刻退出
查日志：`docker logs qq-bot-server`，多半是：
- `SERVER_ADDRESS` 未填或末尾没带 `/`
- `/qq-bot-server/.env` 不存在或没有有效 KEY=VALUE 行
- NapCat 不通：参考主项目 README 里的 NapCat 排错章节

### 2. jm 命令报错 "command not found: jmcomic"
默认配置应该用容器内 PATH 中的 `jmcomic`。如果 `TOOLS_JMCOMIC_BIN` 被 env 覆盖成奇怪的值，先 `unset` 它。

### 3. pdf 转换失败 / pillow 报错
进容器看 Python：

```bash
docker exec -it qq-bot-server bash -lc 'python3 -c "import jmcomic, img2pdf, PIL; print(\"ok\")"'
```

### 4. 想从宿主机看实时日志
`tail -f /qq-bot-server/cache/logs/qq-bot.log` 或者 `docker logs -f qq-bot-server`。

### 5. NapCat 与 QQ-bot 在同一台机器，想走容器内网而不是公网
把 `/qq-bot-server/.env` 里的 `SERVER_ADDRESS` 改成 `http://172.17.0.1:3000/`，docker 默认网桥网关，无需走域名/反代。

---

## 手动部署（无 GitHub Actions）

```bash
# 在你的本机
make build-linux                                       # 产出 build/app
rsync -az --exclude '.git' --exclude 'package/*.exe' \
  ./ root@47.94.197.213:/root/qq-bot-server/

# 在服务器
ssh root@47.94.197.213 '
  cd ~/qq-bot-server
  grep -E "^[A-Za-z_][A-Za-z0-9_]*=" /qq-bot-server/.env > /tmp/.env.qq-bot.docker
  mkdir -p /qq-bot-server/cache/{jm,pdf,logs}
  docker build -t qq-bot-server:latest -f Dockerfile .
  docker stop qq-bot-server 2>/dev/null; docker rm qq-bot-server 2>/dev/null
  docker run -d --name qq-bot-server --restart unless-stopped \
    --env-file /tmp/.env.qq-bot.docker \
    -v /qq-bot-server/cache:/tmp/qq-bot \
    qq-bot-server:latest
'
```
