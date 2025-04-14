# QQ-bot
用go写的qqbot，正在慢慢写一些常用功能，敬请期待。
## 目前主要功能：
* **jm 番号 章节[默认为1]**（返回pdf)
* **pix 关键词[可留空，r18] r18[默认留空]** （返回图片）
* **help 功能[jm,pix等]**（返回指令使用格式）
## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=xiao-en-5970/QQ-bot&type=Timeline)](https://www.star-history.com/#xiao-en-5970/QQ-bot&Timeline)
## 如何启动这个项目:
### 0.准备一台Windows系统的电脑
### 1.准备go 1.22环境 
> [go1.22.10](https://golang.google.cn/dl/go1.22.10.windows-amd64.msi) 
### 2.安装 LiteLoaderQQNT 和LLOneBot
> [LLOneBot快速上手](https://llonebot.com/zh-CN/guide/getting-started)
### 3.将作为bot的号登录带有LLOneBot插件的qq客户端
### 4.简单配置./test.yaml
```yaml
# 日志相关配置
log:
  std_out_log_level: debug #输出到标准输出的log等级(debug/info/warn/error)
  log_level: debug #输出到日志的log等级(debug/info/warn/error)

# api相关配置
server:
  address: "http://localhost:3000/" # qq api请求地址
# pixiv 相关配置
pixiv:
  pixiv_address: "https://image.anosu.top/pixiv/json" #pixiv api请求地址
  size: origin #大小【原图】
# 群聊相关配置
group:
  group_id: # 要监听的群号，为空则从群聊列表or命令行获取
  #例：
  # - 1234567889674
  # - 6546945464656
  # - 5454563523341
  get_group_history_interval: 3 # [单位:秒]进行群消息遍历的间隔时间，间隔越少，bot响应越快，但是消耗资源越多
  update_group_list_interval: 1200 # [单位:秒]更新群列表间隔时间，间隔越少，群列表更新越及时，但是消耗资源越多
  retry: 3 #查询群消息重试次数

# 用户相关配置
user:
  user_id: # qqbot用户id，为空则自动获得当前bot账号

# 缓存相关配置
cache:
  tmp_dir: "./tmp" # jm下载的缓存目标文件夹
  pdf_tmp_dir: "./pdftmp" # 图片合成pdf的缓存文件夹
  max_size: 1024 # [单位:MB]缓存最大值
  clear_interval: 1200 # [单位:秒]清理缓存间隔时间，间隔越少，缓存清理越及时，但是会阻塞文件操作

```

### 5.启动项目
```shell
# 直接启动
go run main.go
#从命令行获取群号
go run main.go GROUP_ID=xxxxxxxxx
```
### 6.观察结果
其他群u输入 @bot jm 350234看看有没有成功返回并上传pdf

## 其他配置
### 1.配置./package/jmoption/opt.yml
> 配置参考 [option常规配置项](https://jmcomic.readthedocs.io/zh-cn/latest/option_file_syntax/)
## 感谢以下项目
* **jm抓取本子** ([JMComic-Crawer_Python](https://github.com/hect0x7/JMComic-Crawler-Python))

* **图片转pdf** ([img2pdf](https://gitlab.mister-muffin.de/josch/img2pdf))

## 最后
**项目还在不断完善，期待更多功能的加入**

