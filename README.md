# QQ-bot
用go写的qqbot，正在慢慢写一些常用功能，敬请期待。
## 目前功能：
* **jm 番号 章节**（返回pdf)

## 如何启动这个项目:
### 0.准备一台Windows系统的电脑
### 1.准备go 1.22环境 
> [go1.22.10](https://golang.google.cn/dl/go1.22.10.windows-amd64.msi) 
### 2.安装 LiteLoaderQQNT 和LLOneBot
> [LLOneBot快速上手](https://llonebot.com/zh-CN/guide/getting-started)
### 3.将作为bot的号登录带有LLOneBot插件的qq客户端
### 4.简单配置./test.yaml
```yaml
# qq api请求地址
address: "http://localhost:3000/"
# 要监听的群号，为空则从命令行获取
group_id:
# 例：
#  - 12312313131
#  - 89657968898
# qqbot用户id，为空则自动获得当前bot账号
user_id:
```

### 5.利用Make启动项目
```shell
# 直接构建
make run
#从命令行获取群号
make run GROUP_ID=xxxxxxxxx
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

