# syntax=docker/dockerfile:1.7
# 注：第一行必须是 syntax 指令，启用 BuildKit 的 --mount=type=cache 特性
# CI 已 export DOCKER_BUILDKIT=1，重复构建时 apt / pip 直接走本地缓存
#
# 镜像职责：跑 CI 交叉编译好的 build/app + jmcomic / img2pdf 两个 Python 工具

FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive

# 默认用阿里云公网镜像（Aliyun ECS / 普通 Linux 都能跑）
# Aliyun ECS 上想再快可以：
#   docker build --build-arg APT_MIRROR=mirrors.cloud.aliyuncs.com \
#                --build-arg PIP_INDEX_URL=https://mirrors.cloud.aliyuncs.com/pypi/simple/ \
#                --build-arg PIP_TRUSTED_HOST=mirrors.cloud.aliyuncs.com ...
ARG APT_MIRROR=mirrors.aliyun.com
ARG PIP_INDEX_URL=https://mirrors.aliyun.com/pypi/simple/
ARG PIP_TRUSTED_HOST=mirrors.aliyun.com

# ---------- apt: 换镜像 + 缓存挂载 ----------
# 1. ubuntu24 默认 sources 是 deb822 (.sources)；同时改一遍 /etc/apt/sources.list 不会有副作用
# 2. /etc/apt/apt.conf.d/docker-clean 默认会在每次 apt-get install 后删 apt 缓存，必须先删它
# 3. /var/cache/apt /var/lib/apt 两个 cache mount 都要挂，第一个放 .deb，第二个放 lists
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    rm -f /etc/apt/apt.conf.d/docker-clean && \
    sed -i "s|//.*archive.ubuntu.com|//${APT_MIRROR}|g; s|//security.ubuntu.com|//${APT_MIRROR}|g" \
        /etc/apt/sources.list /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null || true && \
    apt-get update && apt-get install -y --no-install-recommends \
        tzdata \
        ca-certificates \
        python3 \
        python3-venv \
        python3-pip

ENV TZ=Asia/Shanghai
RUN ln -snf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone

# ---------- python: venv + pip 缓存挂载 + 阿里云源 + 重试 ----------
# - 用 venv，避开 ubuntu24 PEP 668 externally-managed-environment 报错
# - 先 upgrade pip，再装运行时依赖；分层方便缓存
# - --retries 5 --timeout 120 抗弱网（IncompleteRead 主因）
ENV VENV_PATH=/opt/qq-bot-venv
RUN python3 -m venv ${VENV_PATH}

ENV PIP_INDEX_URL=${PIP_INDEX_URL}
ENV PIP_TRUSTED_HOST=${PIP_TRUSTED_HOST}
ENV PIP_DEFAULT_TIMEOUT=120
ENV PIP_RETRIES=5

RUN --mount=type=cache,target=/root/.cache/pip,sharing=locked \
    ${VENV_PATH}/bin/pip install --upgrade pip

RUN --mount=type=cache,target=/root/.cache/pip,sharing=locked \
    ${VENV_PATH}/bin/pip install \
        jmcomic \
        img2pdf \
        pillow \
        pyyaml

ENV PATH="${VENV_PATH}/bin:${PATH}"

# ---------- app ----------
WORKDIR /app

# Go 二进制由 CI 在 Actions runner 上交叉编译好，直接 COPY 进来
COPY build/app ./
# 仅 jm 配置文件进镜像；.exe / .py 工具留给 Windows 本地开发，Linux 走 PATH 中的 jmcomic / img2pdf
COPY package/jmoption ./package/jmoption

# 缓存与日志默认路径：/tmp/qq-bot/{jm,pdf,logs}（os.TempDir/qq-bot 下）
# 部署机用 -v /qq-bot-server/cache:/tmp/qq-bot 一次挂出来
RUN mkdir -p /tmp/qq-bot/jm /tmp/qq-bot/pdf /tmp/qq-bot/logs

# 仅文档性声明：bot internal HTTP API（hfut 反向调用）默认 8090，跟 BOT_INTERNAL_API_PORT env 对齐。
# **EXPOSE 不会自动暴露端口给宿主机**——docker-compose 还需要 ports: ["127.0.0.1:8090:8090"]
# 才能让 nginx 反代到这个端口；详见 skill/bot/SKILL.md "绑定 QQ 流程"段。
EXPOSE 8090
CMD ["./app"]
