# 运行 CI/rsync 提供的预编译 Linux 二进制（build/app），不在镜像内编译 Go
# 同时安装 jmcomic + img2pdf 两个 Python 工具，提供 jm 指令所需的运行时
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive

# - tzdata        : 上海时区
# - ca-certificates : NapCat https 调用需要
# - python3 / pip  : 装 jmcomic + img2pdf
# - 用 venv 而不是直接 pip install，避免 ubuntu24 的 PEP 668 externally-managed-environment 报错
RUN apt-get update && apt-get install -y --no-install-recommends \
        tzdata \
        ca-certificates \
        python3 \
        python3-venv \
        python3-pip \
    && rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone

# Python 工具装到独立 venv 里再把 venv 的 bin 放进 PATH，
# jmcomic / img2pdf 命令直接可调（utils/cmd/cmd_jm.go、utils/to_pdf/to_pdf.go 默认走 PATH）
ENV VENV_PATH=/opt/qq-bot-venv
RUN python3 -m venv ${VENV_PATH} \
    && ${VENV_PATH}/bin/pip install --no-cache-dir --upgrade pip \
    && ${VENV_PATH}/bin/pip install --no-cache-dir \
        jmcomic \
        img2pdf \
        pillow \
        pyyaml
ENV PATH="${VENV_PATH}/bin:${PATH}"

WORKDIR /app

# Go 二进制由 CI 在 Actions runner 上交叉编译好，直接 COPY 进来
COPY build/app ./
# 仅 jm 配置文件进镜像；.exe / .py 工具留给 Windows 本地开发，Linux 走 PATH 中的 jmcomic / img2pdf
COPY package/jmoption ./package/jmoption

# 缓存与日志默认路径：/tmp/qq-bot/{jm,pdf,logs}（os.TempDir/qq-bot 下）
# 部署机用 -v /qq-bot-server/cache:/tmp/qq-bot 一次挂出来
RUN mkdir -p /tmp/qq-bot/jm /tmp/qq-bot/pdf /tmp/qq-bot/logs

# 不开放任何端口（轮询型 bot，不接收外部请求）
CMD ["./app"]
