# QQ-bot
用 go 写的 QQ Bot，慢慢添加常用功能。
> 已从 LLOneBot 迁移到 [NapCat](https://napcat.napneko.icu/) 框架，接口规范参考 [NapCat 接口文档](https://napcat.apifox.cn/)。两端都是 OneBot 11 实现，常用接口完全兼容。

## 目前主要功能：
* **jm 番号 章节[默认为1]**（返回pdf)
* **pix 关键词[可留空，r18] r18[默认留空]** （返回图片）
* **help 功能[jm,pix等]**（返回指令使用格式）
* **github**（返回开源仓库链接）

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=xiao-en-5970/QQ-bot&type=Date)](https://www.star-history.com/#xiao-en-5970/QQ-bot&Date)

## 如何启动这个项目

### 0. 准备 NapCat 运行环境
跟随官方文档安装 NapCat（推荐 Docker 或 Shell 一键脚本）：
> [NapCat 安装指南](https://napcat.napneko.icu/guide/start-install)

### 1. 在 NapCat 中开启 OneBot11 HTTP 服务
- 启动 NapCat 后用机器人账号登录。
- 在 WebUI -> 网络配置（或编辑 `napcat.json`）里启用 **HTTP 服务**，建议使用默认端口 `3000`。
- 如果开启了 `token`，请记录下来，等下要填到 `test.yaml` 的 `server.access_token`。

### 2. 准备 Go 1.22 环境
> [go1.22.10](https://golang.google.cn/dl/go1.22.10.windows-amd64.msi)

### 3. 配置 `./test.yaml`
```yaml
log:
  std_out_log_level: debug
  log_level: debug

server:
  address: "http://localhost:3000/" # NapCat HTTP 服务地址，结尾带斜杠
  access_token: ""                  # NapCat 的 onebot11 HTTP token；不填表示未启用

pixiv:
  pixiv_address: "https://image.anosu.top/pixiv/json"
  size: origin

group:
  group_id:
  get_group_history_interval: 3
  update_group_list_interval: 1200
  retry: 3

user:
  user_id:

cache:
  tmp_dir: "./tmp"
  pdf_tmp_dir: "./pdftmp"
  max_size: 1024
  clear_interval: 1200
```

### 4. 启动项目
```shell
# 直接启动
go run main.go
# 从命令行获取群号
go run main.go GROUP_ID=xxxxxxxxx
```
启动时会先调用 NapCat 的 `/get_status` 做连通性检查，如果失败会打 warning 但仍然继续运行。

### 5. 观察结果
在群里输入 `@bot jm 350234`，看看是否成功返回并上传 pdf。

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
* **bot 框架** ([NapCat](https://github.com/NapNeko/NapCatQQ))
* **jm抓取本子** ([JMComic-Crawer_Python](https://github.com/hect0x7/JMComic-Crawler-Python))
* **图片转pdf** ([img2pdf](https://gitlab.mister-muffin.de/josch/img2pdf))

## 最后
**项目还在不断完善，期待更多功能的加入**
